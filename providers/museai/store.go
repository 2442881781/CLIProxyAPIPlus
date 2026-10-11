package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-plugin-museai/internal/browser"
)

type mediaRecord struct {
	Name      string `json:"name"`
	AuthID    string `json:"auth_id,omitempty"`
	MIME      string `json:"mime"`
	Size      int    `json:"size"`
	CreatedAt int64  `json:"created_at"`
}

type videoTask struct {
	ID          string        `json:"id"`
	AuthID      string        `json:"auth_id"`
	Model       string        `json:"model"`
	Status      string        `json:"status"`
	CreatedAt   int64         `json:"created_at"`
	CompletedAt int64         `json:"completed_at,omitempty"`
	ThreadURL   string        `json:"thread_url,omitempty"`
	Submitted   bool          `json:"submitted,omitempty"`
	Error       string        `json:"error,omitempty"`
	Media       []mediaRecord `json:"media,omitempty"`
	Seconds     string        `json:"seconds,omitempty"`
	Size        string        `json:"size,omitempty"`
}

type taskStore struct {
	mu      sync.Mutex
	dir     string
	tasks   map[string]videoTask
	media   map[string]mediaRecord
	running map[string]bool
}

func newID() string {
	var buf [16]byte
	if _, errRead := rand.Read(buf[:]); errRead != nil {
		panic("random source unavailable")
	}
	return hex.EncodeToString(buf[:])
}

func openTaskStore(dir string) (*taskStore, error) {
	s := &taskStore{dir: dir, tasks: make(map[string]videoTask), media: make(map[string]mediaRecord), running: make(map[string]bool)}
	if errMkdir := os.MkdirAll(filepath.Join(dir, "media"), 0o700); errMkdir != nil {
		return nil, fmt.Errorf("create media directory: %w", errMkdir)
	}
	raw, errRead := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if errRead == nil {
		var saved struct {
			Tasks map[string]videoTask   `json:"tasks"`
			Media map[string]mediaRecord `json:"media"`
		}
		if errDecode := json.Unmarshal(raw, &saved); errDecode != nil {
			return nil, fmt.Errorf("decode Muse task state: %w", errDecode)
		}
		if saved.Tasks != nil {
			s.tasks = saved.Tasks
		}
		if saved.Media != nil {
			s.media = saved.Media
		}
	} else if !os.IsNotExist(errRead) {
		return nil, fmt.Errorf("read Muse task state: %w", errRead)
	}
	changed := false
	for id, task := range s.tasks {
		if task.Status != "completed" && task.Status != "failed" && task.ThreadURL == "" {
			task.Status, task.Error = "failed", "Process restarted before a resumable thread was recorded; submission result is unconfirmed and was not repeated"
			s.tasks[id], changed = task, true
		}
	}
	if changed {
		if errSave := s.persistLocked(); errSave != nil {
			return nil, errSave
		}
	}
	return s, nil
}

func (s *taskStore) persistLocked() error {
	raw, errMarshal := json.Marshal(struct {
		Tasks map[string]videoTask   `json:"tasks"`
		Media map[string]mediaRecord `json:"media"`
	}{s.tasks, s.media})
	if errMarshal != nil {
		return errMarshal
	}
	return atomicPrivateWrite(filepath.Join(s.dir, "tasks.json"), raw)
}

func atomicPrivateWrite(path string, data []byte) error {
	file, errCreate := os.CreateTemp(filepath.Dir(path), ".muse-*")
	if errCreate != nil {
		return fmt.Errorf("create private state: %w", errCreate)
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, errWrite := file.Write(data); errWrite != nil {
		_ = file.Close()
		return fmt.Errorf("write private state: %w", errWrite)
	}
	if errSync := file.Sync(); errSync != nil {
		_ = file.Close()
		return fmt.Errorf("sync private state: %w", errSync)
	}
	if errClose := file.Close(); errClose != nil {
		return fmt.Errorf("close private state: %w", errClose)
	}
	if errRename := os.Rename(temp, path); errRename != nil {
		return fmt.Errorf("replace private state: %w", errRename)
	}
	return nil
}

func (s *taskStore) put(task videoTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.tasks[task.ID]
	task.Media = append([]mediaRecord(nil), task.Media...)
	s.tasks[task.ID] = task
	if errSave := s.persistLocked(); errSave != nil {
		if exists {
			s.tasks[task.ID] = previous
		} else {
			delete(s.tasks, task.ID)
		}
		return errSave
	}
	return nil
}

func (s *taskStore) get(id string) (videoTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, exists := s.tasks[id]
	task.Media = append([]mediaRecord(nil), task.Media...)
	return task, exists
}

func (s *taskStore) start(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *taskStore) finish(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

func (s *taskStore) list(authID string) []videoTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]videoTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		if authID != "" && task.AuthID != authID {
			continue
		}
		task.AuthID = ""
		task.Media = append([]mediaRecord(nil), task.Media...)
		out = append(out, task)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

func (s *taskStore) removeTask(id, authID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, exists := s.tasks[id]
	if !exists || authID != "" && task.AuthID != authID {
		return requestError("task_not_found", "Video task not found")
	}
	if s.running[id] {
		return requestError("task_running", "Active task cannot be deleted; wait for a terminal state")
	}
	delete(s.tasks, id)
	if errSave := s.persistLocked(); errSave != nil {
		s.tasks[id] = task
		return errSave
	}
	return nil
}

func mediaExtension(mime string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "image/png":
		return ".png", true
	case "image/jpeg":
		return ".jpg", true
	case "image/webp":
		return ".webp", true
	case "image/gif":
		return ".gif", true
	case "video/mp4":
		return ".mp4", true
	case "video/webm":
		return ".webm", true
	case "video/quicktime":
		return ".mov", true
	default:
		return "", false
	}
}

func (s *taskStore) saveMedia(media browser.Media, kind, authID string) (mediaRecord, error) {
	ext, valid := mediaExtension(media.MIME)
	if !valid || !strings.HasPrefix(media.MIME, kind+"/") || len(media.Data) == 0 {
		return mediaRecord{}, requestError("invalid_media", "Generated media MIME does not match requested capability")
	}
	record := mediaRecord{Name: newID() + ext, AuthID: authID, MIME: media.MIME, Size: len(media.Data), CreatedAt: time.Now().Unix()}
	if errWrite := atomicPrivateWrite(filepath.Join(s.dir, "media", record.Name), media.Data); errWrite != nil {
		return mediaRecord{}, errWrite
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.media[record.Name] = record
	if errSave := s.persistLocked(); errSave != nil {
		delete(s.media, record.Name)
		_ = os.Remove(filepath.Join(s.dir, "media", record.Name))
		return mediaRecord{}, errSave
	}
	return record, nil
}

func (s *taskStore) mediaList(authID string) []mediaRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]mediaRecord, 0, len(s.media))
	for _, record := range s.media {
		if authID != "" && record.AuthID != authID {
			continue
		}
		record.AuthID = ""
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func (s *taskStore) readMedia(name, authID string) ([]byte, mediaRecord, error) {
	if filepath.Base(name) != name || strings.ContainsAny(name, "\\/\x00") || name == "" {
		return nil, mediaRecord{}, requestError("invalid_media", "Invalid media name")
	}
	s.mu.Lock()
	record, exists := s.media[name]
	s.mu.Unlock()
	if !exists || authID != "" && record.AuthID != authID {
		return nil, record, requestError("media_not_found", "Media not found")
	}
	data, errRead := os.ReadFile(filepath.Join(s.dir, "media", name))
	if errRead != nil {
		return nil, record, fmt.Errorf("read generated media: %w", errRead)
	}
	return data, record, nil
}

func (s *taskStore) removeMedia(name, authID string) error {
	if filepath.Base(name) != name || strings.ContainsAny(name, "\\/\x00") || name == "" {
		return requestError("invalid_media", "Invalid media name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.media[name]
	if !exists || authID != "" && record.AuthID != authID {
		return requestError("media_not_found", "Media not found")
	}
	if errRemove := os.Remove(filepath.Join(s.dir, "media", name)); errRemove != nil && !os.IsNotExist(errRemove) {
		return fmt.Errorf("remove generated media: %w", errRemove)
	}
	delete(s.media, name)
	return s.persistLocked()
}
