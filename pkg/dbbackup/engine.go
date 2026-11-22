// Package dbbackup backs up the databases that run in containers: it finds
// PostgreSQL, MySQL and MariaDB among the running containers, dumps them
// through the Docker exec API into gzip files of the hub's data directory, on
// demand or on a schedule, and can restore a dump into its container. Every
// route is admin-only and passes the switches of the Containers view.
package dbbackup

import (
	"errors"
	"strings"
)

// Engines.
const (
	Postgres = "postgres"
	MySQL    = "mysql"
	MariaDB  = "mariadb"
)

// imageEngines maps the last component of an image repository to an engine.
// The name is matched whole: "postgres-exporter" is not a database.
var imageEngines = map[string]string{
	"postgres": Postgres, "postgresql": Postgres, "postgis": Postgres,
	"timescaledb": Postgres, "timescaledb-ha": Postgres, "pgvector": Postgres,
	"mariadb": MariaDB,
	"mysql":   MySQL, "mysql-server": MySQL, "percona": MySQL, "percona-server": MySQL,
}

// engineFromImage reads the engine in an image reference such as
// "registry.example/team/postgres:16-alpine".
func engineFromImage(image string) string {
	ref := strings.ToLower(image)
	if at := strings.IndexByte(ref, '@'); at >= 0 {
		ref = ref[:at]
	}
	if slash := strings.LastIndexByte(ref, '/'); slash >= 0 {
		ref = ref[slash+1:]
	}
	if colon := strings.IndexByte(ref, ':'); colon >= 0 {
		ref = ref[:colon]
	}
	return imageEngines[ref]
}

// engineFromEnv reads the engine in the names of the variables the official
// images define themselves. An application that merely connects to a
// database has POSTGRES_PASSWORD or MYSQL_HOST, not these.
func engineFromEnv(env []string) string {
	names := make(map[string]bool, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		names[name] = true
	}
	switch {
	case names["PGDATA"] && names["PG_MAJOR"]:
		return Postgres
	case names["MARIADB_VERSION"] || names["MARIADB_MAJOR"]:
		return MariaDB
	case names["MYSQL_MAJOR"] || names["MYSQL_VERSION"]:
		return MySQL
	}
	return ""
}

// detectEngine names the engine of a container, or "".
func detectEngine(image string, env []string) string {
	if engine := engineFromImage(image); engine != "" {
		return engine
	}
	return engineFromEnv(env)
}

func lookupEnv(env []string, names ...string) (string, bool) {
	for _, wanted := range names {
		for _, entry := range env {
			if name, value, ok := strings.Cut(entry, "="); ok && name == wanted {
				return value, true
			}
		}
	}
	return "", false
}

// command is one program to run in the container. password, when set, is
// also in env: it is kept apart to be scrubbed from anything shown.
type command struct {
	argv     []string
	env      []string
	password string
}

var errBadUser = errors.New("le nom d’utilisateur de la base, lu dans l’environnement du conteneur, est inutilisable")

// validUser accepts what a database user can be without ever being read as
// an option or a second argument.
func validUser(user string) bool {
	if user == "" || len(user) > 128 {
		return false
	}
	for _, r := range user {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// credentials reads the account the dump runs as from the container's own
// environment. The password never joins the arguments: it travels in the
// environment of the exec (PGPASSWORD, MYSQL_PWD).
func credentials(engine string, env []string) (user, passwordVar, password string, err error) {
	switch engine {
	case Postgres:
		user = "postgres"
		if value, ok := lookupEnv(env, "POSTGRES_USER", "POSTGRESQL_USERNAME"); ok && value != "" {
			user = value
		}
		password, _ = lookupEnv(env, "POSTGRES_PASSWORD", "POSTGRESQL_PASSWORD")
		passwordVar = "PGPASSWORD"
	case MySQL, MariaDB:
		user = "root"
		value, ok := lookupEnv(env, "MARIADB_ROOT_PASSWORD", "MYSQL_ROOT_PASSWORD")
		if ok && value != "" {
			password = value
		} else if name, hasUser := lookupEnv(env, "MARIADB_USER", "MYSQL_USER"); hasUser && name != "" {
			// No root password in the environment: the application account,
			// which may lack the rights to dump every database.
			if secret, hasSecret := lookupEnv(env, "MARIADB_PASSWORD", "MYSQL_PASSWORD"); hasSecret && secret != "" {
				user, password = name, secret
			}
		}
		passwordVar = "MYSQL_PWD"
	default:
		return "", "", "", errors.New("moteur de base inconnu")
	}
	if !validUser(user) {
		return "", "", "", errBadUser
	}
	return user, passwordVar, password, nil
}

func withPassword(argv []string, passwordVar, password string) command {
	c := command{argv: argv, password: password}
	if password != "" {
		c.env = []string{passwordVar + "=" + password}
	}
	return c
}

// dumpCommands returns the dump programs to try, in order: the next one is
// only tried when the previous does not exist in the image.
func dumpCommands(engine string, env []string) ([]command, error) {
	user, passwordVar, password, err := credentials(engine, env)
	if err != nil {
		return nil, err
	}
	if engine == Postgres {
		return []command{withPassword([]string{
			"pg_dumpall", "--username=" + user, "--clean", "--if-exists", "--no-password",
		}, passwordVar, password)}, nil
	}
	tools := []string{"mysqldump", "mariadb-dump"}
	if engine == MariaDB {
		tools = []string{"mariadb-dump", "mysqldump"}
	}
	var out []command
	for _, tool := range tools {
		out = append(out, withPassword([]string{
			tool, "--user=" + user, "--all-databases", "--single-transaction", "--routines", "--events",
		}, passwordVar, password))
	}
	return out, nil
}

// restoreCommands returns the clients that read a dump on their standard
// input, in the order to try them.
func restoreCommands(engine string, env []string) ([]command, error) {
	user, passwordVar, password, err := credentials(engine, env)
	if err != nil {
		return nil, err
	}
	if engine == Postgres {
		return []command{withPassword([]string{
			"psql", "--username=" + user, "--dbname=postgres", "--no-psqlrc", "--quiet", "--no-password",
		}, passwordVar, password)}, nil
	}
	tools := []string{"mysql", "mariadb"}
	if engine == MariaDB {
		tools = []string{"mariadb", "mysql"}
	}
	var out []command
	for _, tool := range tools {
		out = append(out, withPassword([]string{tool, "--user=" + user}, passwordVar, password))
	}
	return out, nil
}

// scrub removes a password from a text about to be stored or shown.
func scrub(text, password string) string {
	if len(password) < 3 {
		return text
	}
	return strings.ReplaceAll(text, password, "***")
}
