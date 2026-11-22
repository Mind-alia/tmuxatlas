package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const (
	maxPathLength  = 1024
	maxConfigFiles = 16
)

var (
	// projectPattern is the shape of a Compose project name.
	projectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	// recordIDPattern is the shape of a deployment ID, the name of its file.
	recordIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f]{8}$`)
	// imageIDPattern is a full image ID.
	imageIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// imageRefPattern is a repository with an optional tag, as docker tag takes it.
	imageRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,199}(:[0-9]{1,5}/[a-z0-9._/-]{1,199})?(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?$`)
	// tagUnsafe is what a repository name cannot hold.
	tagUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)
)

// ValidProject tells whether name can be a Compose project name.
func ValidProject(name string) bool {
	return projectPattern.MatchString(name)
}

// checkPath refuses anything but a clean absolute path. A path of this shape
// starts with "/": it can never be read as an option by git or docker.
func checkPath(path string) error {
	switch {
	case path == "":
		return fmt.Errorf("chemin vide")
	case len(path) > maxPathLength:
		return fmt.Errorf("chemin trop long")
	case !filepath.IsAbs(path):
		return fmt.Errorf("« %s » n’est pas un chemin absolu", path)
	case filepath.Clean(path) != path:
		return fmt.Errorf("« %s » n’est pas un chemin normalisé", path)
	}
	for _, r := range path {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return fmt.Errorf("chemin avec un caractère de contrôle")
		}
	}
	return nil
}

func checkDir(path string) error {
	if err := checkPath(path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("dossier « %s » introuvable depuis le hub", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("« %s » n’est pas un dossier", path)
	}
	return nil
}

func checkFile(path string) error {
	if err := checkPath(path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("fichier « %s » introuvable depuis le hub", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("« %s » n’est pas un fichier ordinaire", path)
	}
	return nil
}

// plan is a validated stack: what the commands of a deployment are built from.
type plan struct {
	project     string
	workingDir  string
	configFiles []string
	envFiles    []string
}

// validatePlan checks what the labels of the containers say before any of it
// reaches a command line.
func validatePlan(project, workingDir string, configFiles, envFiles []string) (plan, error) {
	if !ValidProject(project) {
		return plan{}, fmt.Errorf("nom d’application invalide")
	}
	if workingDir == "" {
		return plan{}, fmt.Errorf("les conteneurs ne disent pas dans quel dossier l’application a été lancée")
	}
	if err := checkDir(workingDir); err != nil {
		return plan{}, fmt.Errorf("dossier de travail : %w", err)
	}
	if len(configFiles) == 0 {
		return plan{}, fmt.Errorf("les conteneurs ne disent pas quels fichiers Compose décrivent l’application")
	}
	if len(configFiles) > maxConfigFiles || len(envFiles) > maxConfigFiles {
		return plan{}, fmt.Errorf("trop de fichiers Compose")
	}
	for _, file := range configFiles {
		if err := checkFile(file); err != nil {
			return plan{}, fmt.Errorf("fichier Compose : %w", err)
		}
	}
	for _, file := range envFiles {
		if err := checkFile(file); err != nil {
			return plan{}, fmt.Errorf("fichier d’environnement : %w", err)
		}
	}
	return plan{project: project, workingDir: workingDir, configFiles: configFiles, envFiles: envFiles}, nil
}

// composeArgs builds "docker compose …" for the stack: the project, its
// directory and its files exactly as Compose recorded them when it started
// the containers, then the action. Every value is its own argument.
func (p plan) composeArgs(action ...string) []string {
	args := []string{"compose", "-p", p.project, "--project-directory", p.workingDir}
	for _, file := range p.envFiles {
		args = append(args, "--env-file", file)
	}
	for _, file := range p.configFiles {
		args = append(args, "-f", file)
	}
	return append(args, action...)
}

// retaggable tells whether a container's image reference is a name a tag can
// be moved under: not a bare ID, not a reference pinned by digest.
func retaggable(image string) bool {
	return !strings.Contains(image, "@") && !strings.HasPrefix(image, "sha256:") && imageRefPattern.MatchString(image)
}

// repository returns the reference without its tag nor digest.
func repository(image string) string {
	if i := strings.IndexByte(image, '@'); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndexByte(image, ':'); i > strings.LastIndexByte(image, '/') {
		image = image[:i]
	}
	return image
}

// rollbackTag names the local tag that keeps an image: the repository of the
// service's image with the tag atlas-rollback-<stamp>. An image without a
// usable name gets one from the project and the service.
func rollbackTag(project, service, image, stamp string) string {
	repo := repository(image)
	if strings.HasPrefix(image, "sha256:") || !imageRefPattern.MatchString(repo) {
		name := tagUnsafe.ReplaceAllString(strings.ToLower(project+"-"+service), "-")
		repo = "atlas-rollback/" + strings.Trim(name, "-._")
	}
	return repo + ":atlas-rollback-" + stamp
}
