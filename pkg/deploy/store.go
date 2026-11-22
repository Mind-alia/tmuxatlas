package deploy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// keptRecords is how many records a stack keeps; older ones are deleted.
	keptRecords = 40
	// listedRecords is how many the state lists.
	listedRecords = 15
	maxRecordSize = 2 << 20
)

// Kinds of operation.
const (
	KindDeploy   = "deploy"
	KindRollback = "rollback"
)

// Results of an operation.
const (
	ResultRunning     = "running"
	ResultSuccess     = "success"
	ResultFailed      = "failed"
	ResultCancelled   = "cancelled"
	ResultInterrupted = "interrupted" // the hub stopped before the end
)

// Statuses of a step.
const (
	StepPending = "pending"
	StepRunning = "running"
	StepDone    = "done"
	StepFailed  = "failed"
	StepSkipped = "skipped" // not attempted: an earlier step failed
)

// Names of the steps.
const (
	stepCheck  = "check"  // the working tree is clean and follows a remote branch
	stepFetch  = "fetch"  // git fetch
	stepMerge  = "merge"  // git merge --ff-only
	stepImages = "images" // remember and tag the images in use
	stepRetag  = "retag"  // rollback: put the kept images back under their names
	stepBuild  = "build"  // docker compose build
	stepUp     = "up"     // docker compose up -d
	stepVerify = "verify" // the containers run and are healthy
	stepPrune  = "prune"  // drop the oldest rollback tags of the rebuilt images
)

// What an operation did to the image of a service.
const (
	// ImageRebuilt: the deployment built a new image under the service's
	// image name; the previous one is kept under RollbackTag.
	ImageRebuilt = "rebuilt"
	// ImageUnchanged: the name still designates the same image (pulled from a
	// registry, or rebuilt identical). No rollback tag is kept and a rollback
	// never touches it: other applications may share that name.
	ImageUnchanged = "unchanged"
	// ImageReplaced: a rollback moved the name back to an earlier image; the
	// one it replaced is kept under RollbackTag.
	ImageReplaced = "replaced"
)

// Step is one stage of an operation.
type Step struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Commands lists what was run, as arguments joined by spaces.
	Commands   []string `json:"commands,omitempty"`
	StartedAt  string   `json:"started_at,omitempty"`
	DurationMs int64    `json:"duration_ms"`
	// Output is the end of what the commands printed, secrets masked.
	Output    string `json:"output,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Image is the image one service used when the operation started.
type Image struct {
	Service   string `json:"service"`
	Container string `json:"container"`
	// Image is the reference the container was created from.
	Image   string `json:"image"`
	ImageID string `json:"image_id"`
	// RollbackTag is the local tag that keeps ImageID, "" when there is none
	// (image unchanged, or tag removed since).
	RollbackTag string `json:"rollback_tag,omitempty"`
	// Status is rebuilt, unchanged or replaced; "" until the build is over,
	// and in records written before the distinction existed.
	Status string `json:"status,omitempty"`
}

// restorable tells whether a rollback can put this image back: only an image
// this deployment rebuilt, still kept under a tag, under a name that can move.
func (i Image) restorable() bool {
	return i.Status == ImageRebuilt && i.RollbackTag != "" && isRollbackTag(i.RollbackTag) &&
		imageIDPattern.MatchString(i.ImageID) && retaggable(i.Image)
}

// ServiceCheck is the state of one container at the last verification.
type ServiceCheck struct {
	Service   string `json:"service"`
	Container string `json:"container"`
	State     string `json:"state"`
	Health    string `json:"health,omitempty"`
	// Verdict is ok, waiting or failed.
	Verdict string `json:"verdict"`
	Detail  string `json:"detail,omitempty"`
}

// Record is the persistent trace of one deployment or rollback.
type Record struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Stack string `json:"stack"`
	// Actor is the account that asked.
	Actor      string `json:"actor"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Result     string `json:"result"`
	Error      string `json:"error,omitempty"`

	WorkingDir  string   `json:"working_dir"`
	ConfigFiles []string `json:"config_files"`
	Branch      string   `json:"branch,omitempty"`
	Upstream    string   `json:"upstream,omitempty"`
	// CommitBefore and CommitAfter are the commits of the working directory
	// around the operation. A rollback does not move the code: both are equal.
	CommitBefore *Commit `json:"commit_before,omitempty"`
	CommitAfter  *Commit `json:"commit_after,omitempty"`
	// Images are the images in use before the operation.
	Images []Image `json:"images"`
	// RollbackOf is, for a rollback, the deployment whose images were restored.
	RollbackOf string         `json:"rollback_of,omitempty"`
	Steps      []Step         `json:"steps"`
	Checks     []ServiceCheck `json:"checks,omitempty"`
	// CancelRequested is true once an admin asked to stop a running operation.
	CancelRequested bool `json:"cancel_requested,omitempty"`
}

// clone copies a record deeply enough to be read without the lock.
func (r *Record) clone() Record {
	out := *r
	out.ConfigFiles = append([]string(nil), r.ConfigFiles...)
	out.Images = append([]Image{}, r.Images...)
	out.Checks = append([]ServiceCheck(nil), r.Checks...)
	out.Steps = make([]Step, len(r.Steps))
	for i, step := range r.Steps {
		step.Commands = append([]string(nil), step.Commands...)
		out.Steps[i] = step
	}
	return out
}

// withoutOutput returns the record for a listing: steps without their output.
func (r Record) withoutOutput() Record {
	steps := make([]Step, len(r.Steps))
	for i, step := range r.Steps {
		step.Output, step.Commands = "", nil
		steps[i] = step
	}
	r.Steps = steps
	return r
}

func (r *Record) step(name string) *Step {
	for i := range r.Steps {
		if r.Steps[i].Name == name {
			return &r.Steps[i]
		}
	}
	return nil
}

// newRecordID sorts by date, to the nanosecond: the records of a stack, which
// follow one another, are listed in the order they started.
func newRecordID(now time.Time) string {
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	return now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(suffix[:])
}

// store keeps the records as <dir>/<stack>/<id>.json, readable by the hub's
// account only: the output of a build may hold secrets.
type store struct{ dir string }

var errNoRecord = errors.New("déploiement introuvable")

func (s store) stackDir(stack string) (string, error) {
	if !ValidProject(stack) {
		return "", errors.New("nom d’application invalide")
	}
	return filepath.Join(s.dir, stack), nil
}

func (s store) save(record Record) error {
	dir, err := s.stackDir(record.Stack)
	if err != nil {
		return err
	}
	if !recordIDPattern.MatchString(record.ID) {
		return errors.New("identifiant de déploiement invalide")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(dir, record.ID+".json")
	tmp, err := os.CreateTemp(dir, ".record-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

func (s store) load(stack, id string) (Record, error) {
	dir, err := s.stackDir(stack)
	if err != nil {
		return Record{}, err
	}
	if !recordIDPattern.MatchString(id) {
		return Record{}, errNoRecord
	}
	return readRecord(filepath.Join(dir, id+".json"))
}

func readRecord(path string) (Record, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxRecordSize {
		return Record{}, errNoRecord
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, errNoRecord
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, errNoRecord
	}
	if record.Images == nil {
		record.Images = []Image{}
	}
	return record, nil
}

// ids lists the records of a stack, the most recent first.
func (s store) ids(stack string) []string {
	dir, err := s.stackDir(stack)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if id != entry.Name() && recordIDPattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids
}

// list returns the most recent records of a stack, at most limit.
func (s store) list(stack string, limit int) []Record {
	out := []Record{}
	for _, id := range s.ids(stack) {
		if len(out) >= limit {
			break
		}
		if record, err := s.load(stack, id); err == nil && record.Stack == stack {
			out = append(out, record)
		}
	}
	return out
}

// prune deletes the records beyond keptRecords.
func (s store) prune(stack string) {
	dir, err := s.stackDir(stack)
	if err != nil {
		return
	}
	ids := s.ids(stack)
	for i := keptRecords; i < len(ids); i++ {
		_ = os.Remove(filepath.Join(dir, ids[i]+".json"))
	}
}

// closeInterrupted marks the records a previous hub left running: the hub
// stopped in the middle, and nobody follows their commands any more.
func (s store) closeInterrupted(now time.Time) {
	stacks, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range stacks {
		if !entry.IsDir() || !ValidProject(entry.Name()) {
			continue
		}
		for _, id := range s.ids(entry.Name()) {
			record, err := s.load(entry.Name(), id)
			if err != nil || record.Result != ResultRunning {
				continue
			}
			record.Result = ResultInterrupted
			record.Error = "le hub s’est arrêté pendant l’opération : son issue est inconnue"
			record.FinishedAt = now.UTC().Format(time.RFC3339)
			for i := range record.Steps {
				switch record.Steps[i].Status {
				case StepRunning:
					record.Steps[i].Status = StepFailed
					record.Steps[i].Error = "interrompue par l’arrêt du hub"
				case StepPending:
					record.Steps[i].Status = StepSkipped
				}
			}
			_ = s.save(record)
		}
	}
}
