package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// Quota is the subscription returned by Muse's own Settings query.
// Top-up credits are not token counts; the value label uses the site's units.
type Quota struct {
	Tier struct {
		ID   string `json:"tierId"`
		Name string `json:"name"`
	} `json:"tier"`
	Usage *struct {
		State       string   `json:"state"`
		PercentUsed *float64 `json:"percentUsed"`
		ResetsAt    *int64   `json:"resetsAt"`
	} `json:"usage"`
	StatusSubtitle     string   `json:"statusSubtitle"`
	UsageRowLabel      string   `json:"usageRowLabel"`
	TopupBalance       *float64 `json:"topupBalance"`
	TopupTotal         *float64 `json:"topupTotal"`
	TopupRowValueLabel string   `json:"topupRowValueLabel"`
}

// ReadQuota opens Settings in an isolated account-bound browser. It never
// submits a prompt, starts a task, purchases credits, or refreshes stored cookies.
func (e *Engine) ReadQuota(ctx context.Context, in Input) (quota Quota, err error) {
	defer func() {
		if err == nil {
			return
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			err = &Error{Code: "canceled", Message: "Muse quota query canceled", HTTPStatus: 499}
			return
		}
		var known *Error
		if !errors.As(err, &known) {
			err = &Error{Code: "quota_unconfirmed", Message: "Muse quota response could not be confirmed", HTTPStatus: 502}
		}
	}()
	s, operation, err := e.open(ctx, in)
	if err != nil {
		return quota, err
	}
	defer s.close()
	if err = s.applyCookies(operation, in.Cookies); err != nil {
		return quota, err
	}
	if err = s.page.call(operation, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": captureQuotaJS}, nil); err != nil {
		return quota, err
	}
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err = s.page.call(operation, "Page.getFrameTree", nil, &tree); err != nil {
		return quota, err
	}
	s.page.mainFrame.Store(tree.FrameTree.Frame.ID)
	var navigation struct {
		ErrorText string `json:"errorText"`
	}
	if err = s.page.call(operation, "Page.navigate", map[string]string{"url": museOrigin + "/thread/new"}, &navigation); err != nil {
		return quota, err
	}
	if navigation.ErrorText != "" {
		return quota, &Error{Code: "network", Message: "Muse navigation failed on the selected browser transport", HTTPStatus: 502}
	}
	stage := 0
	for {
		switch s.page.navigationStatus.Load() {
		case 401:
			return quota, &Error{Code: "cookie_expired", Message: "Muse requires fresh login Cookies", HTTPStatus: 401}
		case 403:
			return quota, &Error{Code: "access_denied", Message: "Muse denied access; check account permissions and egress", HTTPStatus: 403}
		case 429:
			return quota, &Error{Code: "rate_limited", Message: "Muse quota query was rate limited", HTTPStatus: 429}
		}
		var observed struct {
			Stage  int             `json:"stage"`
			Status int             `json:"status"`
			Login  bool            `json:"login"`
			Quota  json.RawMessage `json:"quota"`
			Click  *struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"click"`
		}
		if err = s.page.evaluate(operation, quotaStateJS+"("+jsArg(stage)+")", &observed); err != nil {
			return quota, err
		}
		if observed.Login {
			return quota, &Error{Code: "cookie_expired", Message: "Muse requires fresh login Cookies", HTTPStatus: 401}
		}
		if observed.Status != 0 {
			return quota, &Error{Code: "quota_unconfirmed", Message: "Muse subscription query failed", HTTPStatus: observed.Status}
		}
		if len(observed.Quota) > 0 && string(observed.Quota) != "null" {
			if err = json.Unmarshal(observed.Quota, &quota); err != nil {
				return quota, err
			}
			return quota, nil
		}
		stage = observed.Stage
		if observed.Click != nil {
			if err = s.page.call(operation, "Page.bringToFront", nil, nil); err != nil {
				return quota, err
			}
			if err = s.page.call(operation, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": observed.Click.X, "y": observed.Click.Y}, nil); err != nil {
				return quota, err
			}
			// Muse opens Settings on pointer-down; DOM click() alone does not open it.
			for _, eventType := range []string{"mousePressed", "mouseReleased"} {
				buttons := 0
				if eventType == "mousePressed" {
					buttons = 1
				}
				if err = s.page.call(operation, "Input.dispatchMouseEvent", map[string]any{"type": eventType, "x": observed.Click.X, "y": observed.Click.Y, "button": "left", "buttons": buttons, "clickCount": 1}, nil); err != nil {
					return quota, err
				}
			}
		}
		if err = delay(operation, e.cfg.PollInterval); err != nil {
			return quota, err
		}
		if status := s.page.navigationStatus.Load(); status >= http.StatusInternalServerError {
			return quota, &Error{Code: "upstream_unavailable", Message: "Muse returned a server error", HTTPStatus: 502}
		}
	}
}

// Observe structured data rather than guessing percentages from arbitrary DOM
// text. Action IDs change with Muse deployments; let its own client invoke them.
const captureQuotaJS = `(()=>{
const original=window.fetch;
window.__cpaMuseQuota={quota:null,status:0};
window.fetch=async function(...args){
 const response=await original.apply(this,args);
 let target;try{target=new URL(typeof args[0]==='string'?args[0]:args[0].url,location.href);}catch{return response;}
 if(target.origin!==location.origin || !response.headers.get('content-type')?.includes('text/x-component'))return response;
 if(!response.ok){window.__cpaMuseQuota.status=response.status;return response;}
 const options=args[1]||{};if(!new Headers(options.headers).has('Next-Action'))return response;
 response.clone().text().then(text=>{
  for(const line of text.split('\n')){
   const pos=line.indexOf(':');if(pos<0)continue;
   let value;try{value=JSON.parse(line.slice(pos+1));}catch{continue;}
   if(value?.success===true && value.subscription && typeof value.subscription==='object')window.__cpaMuseQuota.quota=value.subscription;
   else if(value?.sessionRecovery)window.__cpaMuseQuota.status=401;
  }
 }).catch(()=>{window.__cpaMuseQuota.status=502;});
 return response;
};
})()`

const quotaStateJS = `(stage=>{
const visible=e=>!!e&&e.getClientRects().length&&getComputedStyle(e).visibility!=='hidden';
const state=window.__cpaMuseQuota||{};
const login=[...document.querySelectorAll('input[type="password"],input[autocomplete="username"],input[autocomplete="email"],input[placeholder="Mobile number or email"]')].some(visible);
if(state.quota||state.status||login)return {...state,stage,login};
const dialog=[...document.querySelectorAll('[role="dialog"],[aria-modal="true"]')].find(visible);
if(dialog)return {...state,stage:2,login};
const hydration=document.querySelector('[data-hatch-shell-hydration-state]');
if(document.readyState!=='complete'||(hydration&&hydration.getAttribute('data-hatch-shell-hydration-state')!=='hydrated'))return {...state,stage,login};
let target;
if(stage===0)target=[...document.querySelectorAll('button[aria-label="Settings"]')].find(visible);
else if(stage===1)target=[...document.querySelectorAll('[role="menuitem"]')].find(e=>visible(e)&&((e.textContent||'').trim()==='Settings'||[...e.querySelectorAll('span,div')].some(label=>!label.children.length&&(label.textContent||'').trim()==='Settings')));
if(target&&!target.disabled){target.scrollIntoView({block:'center',behavior:'instant'});const r=target.getBoundingClientRect(),x=r.x+r.width/2,y=r.y+r.height/2;const hit=document.elementFromPoint(x,y);if(hit&&target.contains(hit)){target.focus();return {...state,stage:stage+1,login,click:{x,y}};}}
return {...state,stage,login};
})`
