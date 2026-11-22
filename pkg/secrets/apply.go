package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"filippo.io/age"
)

var (
	// envFileName is what a target file may be called: .env, .env.<suffix>
	// or <name>.env. Apply replaces the whole file, so it is kept away from
	// anything that is not an environment file (a shell profile, an
	// authorized_keys...).
	envFileName   = regexp.MustCompile(`^(\.env(\.[A-Za-z0-9_-]+)*|[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*\.env)$`)
	containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

const (
	maxPathLength = 1024
	// maxTargetBytes bounds the existing target file read for its backup.
	maxTargetBytes = 4 << 20
)

// within reports whether path is dir or below it. Both are clean.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveTarget validates a target path and returns the real directory and
// the file name to write there. The path must be absolute and clean, named
// like an environment file, in an existing directory that really is (once
// symbolic links are followed) under the hub user's home and outside the
// vault and the other forbidden directories; the file itself must be a
// regular file or not exist — never a symbolic link.
func (v *Vault) resolveTarget(path string) (string, string, error) {
	if path == "" || len(path) > maxPathLength {
		return "", "", invalid("chemin cible invalide")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return "", "", invalid("chemin cible invalide : caractère de contrôle")
		}
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", invalid("le chemin cible doit être absolu et normalisé (sans « .. », « . » ni barre finale)")
	}
	base := filepath.Base(path)
	if !envFileName.MatchString(base) {
		return "", "", invalid("le fichier cible doit s’appeler .env, .env.<suffixe> ou <nom>.env")
	}
	home, err := filepath.EvalSymlinks(v.home)
	if err != nil {
		return "", "", errors.New("secrets: home directory cannot be resolved")
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", "", invalid("le dossier du fichier cible n’existe pas ou n’est pas accessible")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", "", invalid("le dossier du fichier cible n’existe pas ou n’est pas accessible")
	}
	if !within(home, dir) {
		return "", "", invalid("le fichier cible doit se trouver dans le dossier personnel du compte du hub")
	}
	for _, forbidden := range append([]string{v.dir}, v.forbidden...) {
		if within(forbidden, dir) {
			return "", "", invalid("le fichier cible ne peut pas se trouver dans un dossier d’Atlas")
		}
		if resolved, err := filepath.EvalSymlinks(forbidden); err == nil && within(resolved, dir) {
			return "", "", invalid("le fichier cible ne peut pas se trouver dans un dossier d’Atlas")
		}
	}
	info, err := os.Lstat(filepath.Join(dir, base))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return "", "", invalid("le fichier cible n’est pas accessible")
	case info.Mode()&os.ModeSymlink != 0:
		return "", "", invalid("le fichier cible est un lien symbolique : refusé")
	case !info.Mode().IsRegular():
		return "", "", invalid("le fichier cible n’est pas un fichier ordinaire")
	}
	return dir, base, nil
}

// SetTarget sets, or with two empty strings clears, where Apply writes.
func (v *Vault) SetTarget(actor, app, envFile, container string) (App, error) {
	var target *Target
	if envFile != "" || container != "" {
		if envFile == "" {
			return App{}, invalid("un conteneur à redémarrer demande aussi un fichier cible")
		}
		if _, _, err := v.resolveTarget(envFile); err != nil {
			return App{}, err
		}
		if container != "" && !containerName.MatchString(container) {
			return App{}, invalid("nom de conteneur invalide")
		}
		target = &Target{EnvFile: envFile, Container: container}
	}
	return v.update(app, func(doc *appDoc, _ *age.X25519Recipient) (Entry, error) {
		doc.Target = target
		entry := Entry{Actor: actor, Action: "target_clear", App: app}
		if target != nil {
			entry.Action, entry.Detail = "target_set", targetDetail(*target)
		}
		return entry, nil
	})
}

func targetDetail(target Target) string {
	if target.Container == "" {
		return target.EnvFile
	}
	return target.EnvFile + " ; conteneur " + target.Container
}

// ApplyResult says what Apply did.
type ApplyResult struct {
	EnvFile   string `json:"env_file"`
	Variables int    `json:"variables"`
	// Backup is true when the file that was replaced is kept as
	// <env_file>.bak.
	Backup bool `json:"backup"`
	// Unchanged is true when the file already had this exact content.
	Unchanged bool   `json:"unchanged,omitempty"`
	Container string `json:"container,omitempty"`
	Restarted bool   `json:"restarted"`
	// RestartError is set when the file was written but the restart failed.
	RestartError string `json:"restart_error,omitempty"`
}

// Apply writes the application's variables, in clear, to its target file
// (0600, atomically, the older file kept as .bak) and then restarts the
// target container if there is one. The vault must be unlocked by this
// session, and the application must have at least one variable.
func (v *Vault) Apply(actor, session, app string) (ApplyResult, error) {
	if err := ValidateAppName(app); err != nil {
		return ApplyResult{}, err
	}
	result, err := v.writeTarget(actor, session, app)
	if err != nil || result.Container == "" {
		return result, err
	}
	// The restart runs without the vault's lock: it can last a grace period.
	restartErr := errors.New("le redémarrage de conteneurs n’est pas disponible sur ce serveur")
	if v.restarter != nil {
		restartErr = v.restarter.RestartContainer(actor, result.Container)
	}
	entry := Entry{Actor: actor, Action: "restart", App: app, Detail: result.Container}
	if restartErr != nil {
		entry.Action = "restart_failed"
		result.RestartError = restartErr.Error()
	} else {
		result.Restarted = true
	}
	v.mu.Lock()
	v.auditLocked(entry)
	v.mu.Unlock()
	return result, nil
}

func (v *Vault) writeTarget(actor, session, app string) (ApplyResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, err := v.readVault(); err != nil {
		return ApplyResult{}, err
	}
	identity, err := v.keyFor(session)
	if err != nil {
		return ApplyResult{}, err
	}
	doc, err := v.readApp(app)
	if err != nil {
		return ApplyResult{}, err
	}
	if doc.Target == nil || doc.Target.EnvFile == "" {
		return ApplyResult{}, ErrNoTarget
	}
	// An empty application would replace a working file by an empty one.
	if len(doc.Vars) == 0 {
		return ApplyResult{}, invalid("cette application n’a aucune variable : rien à appliquer")
	}
	// The path is checked again now: the directory may have changed since
	// the target was saved.
	dir, base, err := v.openTargetDir(doc.Target.EnvFile)
	if err != nil {
		return ApplyResult{}, err
	}
	defer dir.Close()
	pairs, err := decryptAll(doc, identity)
	if err != nil {
		return ApplyResult{}, err
	}
	content := []byte(FormatDotenv(pairs))
	previous, info, err := readRegular(dir, base)
	if err != nil {
		return ApplyResult{}, err
	}
	result := ApplyResult{EnvFile: doc.Target.EnvFile, Variables: len(pairs), Container: doc.Target.Container}
	if err := v.appendAudit(Entry{Actor: actor, Action: "apply", App: app,
		Detail: doc.Target.EnvFile + " ; " + strconv.Itoa(len(pairs)) + " variables"}); err != nil {
		return ApplyResult{}, err
	}
	if info != nil && bytes.Equal(previous, content) {
		// Nothing to change: the backup, which holds the file from before
		// the last change, is left alone, and so is the file if it is
		// already private.
		result.Unchanged = true
		if info.Mode().Perm() == 0o600 {
			return result, nil
		}
	} else if info != nil {
		if err := keepBackup(dir, base+".bak", previous); err != nil {
			return ApplyResult{}, failure("la copie de sauvegarde (.bak) n’a pas pu être écrite ; rien n’a été modifié")
		}
		result.Backup = true
	}
	if err := writeAtomicIn(dir, base, content); err != nil {
		return ApplyResult{}, failure("le fichier cible n’a pas pu être écrit")
	}
	return result, nil
}

// openTargetDir validates a target path like resolveTarget, then opens its
// directory as an os.Root under the home: what follows is done relative to
// that open directory, so a path component replaced by a link afterwards
// cannot move the write, and nothing can be reached outside the home.
func (v *Vault) openTargetDir(path string) (*os.Root, string, error) {
	resolved, base, err := v.resolveTarget(path)
	if err != nil {
		return nil, "", err
	}
	gone := invalid("le dossier du fichier cible a changé pendant la vérification : réessaie")
	home, err := filepath.EvalSymlinks(v.home)
	if err != nil {
		return nil, "", errors.New("secrets: home directory cannot be resolved")
	}
	rel, err := filepath.Rel(home, resolved)
	if err != nil {
		return nil, "", gone
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, "", errors.New("secrets: home directory cannot be opened")
	}
	defer root.Close()
	dir, err := root.OpenRoot(rel)
	if err != nil {
		return nil, "", gone
	}
	// The directory opened is the one that was validated.
	opened, openedErr := dir.Stat(".")
	checked, checkedErr := os.Stat(resolved)
	if openedErr != nil || checkedErr != nil || !os.SameFile(opened, checked) {
		dir.Close()
		return nil, "", gone
	}
	return dir, base, nil
}

// readRegular reads name in dir if it is a regular file of a bounded size;
// a missing file gives a nil info. A symbolic link is never followed.
func readRegular(dir *os.Root, name string) ([]byte, os.FileInfo, error) {
	info, err := dir.Lstat(name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil, nil
	case err != nil:
		return nil, nil, invalid("le fichier cible n’est pas accessible")
	case info.Mode()&os.ModeSymlink != 0:
		return nil, nil, invalid("le fichier cible est un lien symbolique : refusé")
	case !info.Mode().IsRegular():
		return nil, nil, invalid("le fichier cible n’est pas un fichier ordinaire")
	case info.Size() > maxTargetBytes:
		return nil, nil, invalid("le fichier cible actuel est trop gros (%d Mio au plus) pour être sauvegardé", maxTargetBytes>>20)
	}
	file, err := dir.Open(name)
	if err != nil {
		return nil, nil, invalid("le fichier cible n’est pas lisible")
	}
	defer file.Close()
	// The file opened is the one that was just examined.
	if opened, err := file.Stat(); err != nil || !os.SameFile(info, opened) {
		return nil, nil, invalid("le fichier cible a changé pendant la vérification : réessaie")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxTargetBytes+1))
	if err != nil || len(content) > maxTargetBytes {
		return nil, nil, invalid("le fichier cible n’est pas lisible")
	}
	return content, info, nil
}

// keepBackup stores previous as name (<file>.bak). A backup that already
// holds this content is left as it is; another one is kept one generation
// more as <file>.bak.1 instead of being lost.
func keepBackup(dir *os.Root, name string, previous []byte) error {
	current, info, err := readRegular(dir, name)
	if err == nil && info != nil && bytes.Equal(current, previous) {
		return nil
	}
	if err != nil || info != nil {
		if err := dir.Rename(name, name+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return writeAtomicIn(dir, name, previous)
}

// writeAtomicIn writes a 0600 file in dir through a temporary file and a
// rename. The result is a new file: see docs/secrets.md for what that means
// to a container that mounts the file itself.
func writeAtomicIn(dir *os.Root, name string, content []byte) error {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temp := ".tmp-" + hex.EncodeToString(random)
	file, err := dir.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer dir.Remove(temp)
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(content)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return dir.Rename(temp, name)
}
