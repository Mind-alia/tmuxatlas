package secrets

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
)

// appDoc is apps/<name>.json: names in clear, each value an armored age
// file encrypted for the vault's public key.
type appDoc struct {
	Name      string    `json:"name"`
	Target    *Target   `json:"target,omitempty"`
	Vars      []varDoc  `json:"vars"`
	CreatedAt time.Time `json:"created_at"`
}

type varDoc struct {
	Name      string    `json:"name"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Target is where Apply writes an application's variables.
type Target struct {
	// EnvFile is the absolute path of the dotenv file to write.
	EnvFile string `json:"env_file"`
	// Container, optional, is restarted after the file is written.
	Container string `json:"container,omitempty"`
}

// App is what the API shows of an application: never a value.
type App struct {
	Name      string     `json:"name"`
	Target    *Target    `json:"target,omitempty"`
	Variables []Variable `json:"variables"`
	// Unreadable marks an application whose file is damaged: it can only be
	// deleted.
	Unreadable bool `json:"unreadable,omitempty"`
}

// Variable names one stored value.
type Variable struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (v *Vault) appPath(name string) string {
	return filepath.Join(v.dir, appsDir, name+".json")
}

// readApp reads one application. v.mu is held; name is validated.
func (v *Vault) readApp(name string) (appDoc, error) {
	var doc appDoc
	raw, err := os.ReadFile(v.appPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return doc, ErrAppNotFound
	}
	if err != nil {
		return doc, err
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Name != name || len(doc.Vars) > MaxVariables {
		return doc, ErrCorrupt
	}
	// The file is not trusted more than a request: a name written there by
	// hand would otherwise reach an applied file as it is.
	seen := make(map[string]bool, len(doc.Vars))
	for _, variable := range doc.Vars {
		if ValidateVarName(variable.Name) != nil || seen[variable.Name] || variable.Value == "" {
			return doc, ErrCorrupt
		}
		seen[variable.Name] = true
	}
	if doc.Target != nil && (doc.Target.EnvFile == "" || (doc.Target.Container != "" && !containerName.MatchString(doc.Target.Container))) {
		return doc, ErrCorrupt
	}
	return doc, nil
}

func appView(doc appDoc) App {
	view := App{Name: doc.Name, Target: doc.Target, Variables: make([]Variable, 0, len(doc.Vars))}
	for _, variable := range doc.Vars {
		view.Variables = append(view.Variables, Variable{Name: variable.Name, UpdatedAt: variable.UpdatedAt})
	}
	return view
}

// appNames lists the stored applications. v.mu is held.
func (v *Vault) appNames() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(v.dir, appsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		name, isJSON := strings.CutSuffix(entry.Name(), ".json")
		if isJSON && entry.Type().IsRegular() && ValidateAppName(name) == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Apps lists the applications with the names of their variables.
func (v *Vault) Apps() ([]App, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, err := v.readVault(); err != nil {
		return nil, err
	}
	names, err := v.appNames()
	if err != nil {
		return nil, err
	}
	apps := make([]App, 0, len(names))
	for _, name := range names {
		doc, err := v.readApp(name)
		switch {
		case errors.Is(err, ErrCorrupt):
			// One damaged file does not hide the other applications.
			apps = append(apps, App{Name: name, Variables: []Variable{}, Unreadable: true})
		case errors.Is(err, ErrAppNotFound):
		case err != nil:
			return nil, err
		default:
			apps = append(apps, appView(doc))
		}
	}
	return apps, nil
}

// update reads an application under the vault's locks, lets fn change it and
// writes it back atomically. The recipient is given so fn can encrypt. The
// entry fn returns goes to the audit trail once the file is written, so the
// trail never tells of a change that did not happen.
func (v *Vault) update(name string, fn func(doc *appDoc, recipient *age.X25519Recipient) (Entry, error)) (App, error) {
	if err := ValidateAppName(name); err != nil {
		return App{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var view App
	err := v.withFileLock(func() error {
		recipient, err := v.recipient()
		if err != nil {
			return err
		}
		doc, err := v.readApp(name)
		if err != nil {
			return err
		}
		entry, err := fn(&doc, recipient)
		if err != nil {
			return err
		}
		if err := writeJSONFile(v.appPath(name), doc); err != nil {
			return err
		}
		v.auditLocked(entry)
		view = appView(doc)
		return nil
	})
	return view, err
}

// CreateApp adds an empty application.
func (v *Vault) CreateApp(actor, name string) (App, error) {
	if err := ValidateAppName(name); err != nil {
		return App{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	doc := appDoc{Name: name, Vars: []varDoc{}, CreatedAt: v.now().UTC()}
	err := v.withFileLock(func() error {
		if _, err := v.readVault(); err != nil {
			return err
		}
		if _, err := v.readApp(name); err == nil {
			return ErrAppExists
		} else if !errors.Is(err, ErrAppNotFound) {
			return err
		}
		names, err := v.appNames()
		if err != nil {
			return err
		}
		if len(names) >= MaxApps {
			return invalid("trop d’applications (%d au plus)", MaxApps)
		}
		if err := os.MkdirAll(filepath.Join(v.dir, appsDir), 0o700); err != nil {
			return err
		}
		if err := writeJSONFile(v.appPath(name), doc); err != nil {
			return err
		}
		v.auditLocked(Entry{Actor: actor, Action: "app_create", App: name})
		return nil
	})
	return appView(doc), err
}

// DeleteApp removes an application and its encrypted values. Files already
// applied are left alone.
func (v *Vault) DeleteApp(actor, name string) error {
	if err := ValidateAppName(name); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.withFileLock(func() error {
		// A damaged application can still be deleted.
		if _, err := v.readApp(name); err != nil && !errors.Is(err, ErrCorrupt) {
			return err
		}
		if err := os.Remove(v.appPath(name)); err != nil {
			return err
		}
		v.auditLocked(Entry{Actor: actor, Action: "app_delete", App: name})
		return nil
	})
}

// setVar encrypts value and stores it in doc, replacing an older one.
func setVar(doc *appDoc, recipient *age.X25519Recipient, name, value string, now time.Time) error {
	sealed, err := encryptArmored([]byte(value), recipient)
	if err != nil {
		return err
	}
	entry := varDoc{Name: name, Value: sealed, UpdatedAt: now}
	for i := range doc.Vars {
		if doc.Vars[i].Name == name {
			doc.Vars[i] = entry
			return nil
		}
	}
	if len(doc.Vars) >= MaxVariables {
		return invalid("trop de variables dans cette application (%d au plus)", MaxVariables)
	}
	doc.Vars = append(doc.Vars, entry)
	return nil
}

// SetVar stores one value, encrypted at once. The vault may be locked.
func (v *Vault) SetVar(actor, app, name, value string) (App, error) {
	if err := ValidateVarName(name); err != nil {
		return App{}, err
	}
	if err := validateValue(value); err != nil {
		return App{}, err
	}
	return v.update(app, func(doc *appDoc, recipient *age.X25519Recipient) (Entry, error) {
		return Entry{Actor: actor, Action: "var_set", App: app, Name: name},
			setVar(doc, recipient, name, value, v.now().UTC())
	})
}

// DeleteVar removes one variable.
func (v *Vault) DeleteVar(actor, app, name string) (App, error) {
	if err := ValidateVarName(name); err != nil {
		return App{}, err
	}
	return v.update(app, func(doc *appDoc, _ *age.X25519Recipient) (Entry, error) {
		for i := range doc.Vars {
			if doc.Vars[i].Name == name {
				doc.Vars = append(doc.Vars[:i], doc.Vars[i+1:]...)
				return Entry{Actor: actor, Action: "var_delete", App: app, Name: name}, nil
			}
		}
		return Entry{}, ErrVarNotFound
	})
}

// ImportResult names what an import stored.
type ImportResult struct {
	App      App      `json:"app"`
	Imported []string `json:"imported"`
}

// Import parses a pasted dotenv text and stores every value encrypted. A
// text with one invalid line stores nothing. The vault may be locked.
func (v *Vault) Import(actor, app, text string) (ImportResult, error) {
	if len(text) > MaxImportBytes {
		return ImportResult{}, invalid("texte trop long (%d Kio au plus)", MaxImportBytes>>10)
	}
	pairs, err := ParseDotenv(text)
	if err != nil {
		return ImportResult{}, err
	}
	if len(pairs) == 0 {
		return ImportResult{}, invalid("aucune variable trouvée dans ce texte")
	}
	result := ImportResult{Imported: make([]string, 0, len(pairs))}
	for _, pair := range pairs {
		if err := validateValue(pair.Value); err != nil {
			return ImportResult{}, invalid("variable %s : %s", pair.Name, err.Error())
		}
		result.Imported = append(result.Imported, pair.Name)
	}
	result.App, err = v.update(app, func(doc *appDoc, recipient *age.X25519Recipient) (Entry, error) {
		now := v.now().UTC()
		for _, pair := range pairs {
			if err := setVar(doc, recipient, pair.Name, pair.Value, now); err != nil {
				return Entry{}, err
			}
		}
		return Entry{Actor: actor, Action: "import", App: app, Detail: strconv.Itoa(len(pairs)) + " variables"}, nil
	})
	if err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

// decryptAll returns the clear pairs of doc, in order.
func decryptAll(doc appDoc, identity *age.X25519Identity) ([]Pair, error) {
	pairs := make([]Pair, 0, len(doc.Vars))
	for _, variable := range doc.Vars {
		plain, err := decryptArmored(variable.Value, identity)
		if err != nil || len(plain) > MaxValueBytes {
			// The age error is dropped: only the name is reported.
			return nil, invalid("la variable %s ne se déchiffre pas avec la clé du coffre", variable.Name)
		}
		pairs = append(pairs, Pair{Name: variable.Name, Value: string(plain)})
	}
	return pairs, nil
}

// Reveal decrypts one value. The vault must be unlocked by this session, and
// the reading is written to the audit trail before the value is returned.
func (v *Vault) Reveal(actor, session, app, name string) (string, error) {
	if err := ValidateAppName(app); err != nil {
		return "", err
	}
	if err := ValidateVarName(name); err != nil {
		return "", err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, err := v.readVault(); err != nil {
		return "", err
	}
	identity, err := v.keyFor(session)
	if err != nil {
		return "", err
	}
	doc, err := v.readApp(app)
	if err != nil {
		return "", err
	}
	for _, variable := range doc.Vars {
		if variable.Name != name {
			continue
		}
		pairs, err := decryptAll(appDoc{Vars: []varDoc{variable}}, identity)
		if err != nil {
			return "", err
		}
		if err := v.appendAudit(Entry{Actor: actor, Action: "reveal", App: app, Name: name}); err != nil {
			return "", err
		}
		return pairs[0].Value, nil
	}
	return "", ErrVarNotFound
}

// Export returns the application as a dotenv text. The vault must be
// unlocked by this session; the export is audited first.
func (v *Vault) Export(actor, session, app string) (string, error) {
	if err := ValidateAppName(app); err != nil {
		return "", err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, err := v.readVault(); err != nil {
		return "", err
	}
	identity, err := v.keyFor(session)
	if err != nil {
		return "", err
	}
	doc, err := v.readApp(app)
	if err != nil {
		return "", err
	}
	pairs, err := decryptAll(doc, identity)
	if err != nil {
		return "", err
	}
	if err := v.appendAudit(Entry{Actor: actor, Action: "export", App: app, Detail: strconv.Itoa(len(pairs)) + " variables"}); err != nil {
		return "", err
	}
	return FormatDotenv(pairs), nil
}
