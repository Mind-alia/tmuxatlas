package secrets

import (
	"errors"
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

const maxPathLength = 1024

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
	return v.update(app, func(doc *appDoc, _ *age.X25519Recipient) error {
		doc.Target = target
		entry := Entry{Actor: actor, Action: "target_clear", App: app}
		if target != nil {
			entry.Action, entry.Detail = "target_set", targetDetail(*target)
		}
		v.auditLocked(entry)
		return nil
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
	// Backup is true when an older file was kept as <env_file>.bak.
	Backup    bool   `json:"backup"`
	Container string `json:"container,omitempty"`
	Restarted bool   `json:"restarted"`
	// RestartError is set when the file was written but the restart failed.
	RestartError string `json:"restart_error,omitempty"`
}

// Apply writes the application's variables, in clear, to its target file
// (0600, atomically, the older file kept as .bak) and then restarts the
// target container if there is one. The vault must be unlocked.
func (v *Vault) Apply(actor, app string) (ApplyResult, error) {
	if err := ValidateAppName(app); err != nil {
		return ApplyResult{}, err
	}
	result, err := v.writeTarget(actor, app)
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

func (v *Vault) writeTarget(actor, app string) (ApplyResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, err := v.readVault(); err != nil {
		return ApplyResult{}, err
	}
	identity := v.unlockedLocked()
	if identity == nil {
		return ApplyResult{}, ErrLocked
	}
	doc, err := v.readApp(app)
	if err != nil {
		return ApplyResult{}, err
	}
	if doc.Target == nil || doc.Target.EnvFile == "" {
		return ApplyResult{}, ErrNoTarget
	}
	if doc.Target.Container != "" && !containerName.MatchString(doc.Target.Container) {
		return ApplyResult{}, invalid("nom de conteneur invalide")
	}
	// The path is checked again now: the directory may have changed since
	// the target was saved.
	dir, base, err := v.resolveTarget(doc.Target.EnvFile)
	if err != nil {
		return ApplyResult{}, err
	}
	pairs, err := decryptAll(doc, identity)
	if err != nil {
		return ApplyResult{}, err
	}
	result := ApplyResult{EnvFile: doc.Target.EnvFile, Variables: len(pairs), Container: doc.Target.Container}
	if err := v.appendAudit(Entry{Actor: actor, Action: "apply", App: app,
		Detail: doc.Target.EnvFile + " ; " + strconv.Itoa(len(pairs)) + " variables"}); err != nil {
		return ApplyResult{}, err
	}
	file := filepath.Join(dir, base)
	previous, err := os.ReadFile(file)
	switch {
	case err == nil:
		if err := writeFileAtomic(file+".bak", previous); err != nil {
			return ApplyResult{}, failure("la copie de sauvegarde (.bak) n’a pas pu être écrite ; rien n’a été modifié")
		}
		result.Backup = true
	case !errors.Is(err, os.ErrNotExist):
		return ApplyResult{}, invalid("le fichier cible n’est pas lisible")
	}
	if err := writeFileAtomic(file, []byte(FormatDotenv(pairs))); err != nil {
		return ApplyResult{}, failure("le fichier cible n’a pas pu être écrit")
	}
	return result, nil
}
