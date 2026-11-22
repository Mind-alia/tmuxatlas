package dbbackup

import (
	"strings"
	"testing"
	"time"
)

func TestDetectEngine(t *testing.T) {
	for _, test := range []struct {
		image string
		env   []string
		want  string
	}{
		{"postgres:16-alpine", nil, Postgres},
		{"registry.example/team/postgres:15", nil, Postgres},
		{"postgis/postgis:16-3.4", nil, Postgres},
		{"pgvector/pgvector:pg16", nil, Postgres},
		{"bitnami/postgresql@sha256:abcdef", nil, Postgres},
		{"mysql:8.4", nil, MySQL},
		{"percona:8", nil, MySQL},
		{"mariadb:11", nil, MariaDB},
		{"library/MariaDB:10.6", nil, MariaDB},
		// Tools that talk to a database are not one.
		{"prometheuscommunity/postgres-exporter:latest", nil, ""},
		{"dpage/pgadmin4", nil, ""},
		{"phpmyadmin", nil, ""},
		{"nginx:1.27", nil, ""},
		// An application that connects to a database only has its address.
		{"shop/api:3", []string{"POSTGRES_PASSWORD=x", "POSTGRES_HOST=db", "MYSQL_HOST=db"}, ""},
		// A rebuilt image keeps the variables of the official one.
		{"shop/custom-db:1", []string{"PGDATA=/var/lib/postgresql/data", "PG_MAJOR=16"}, Postgres},
		{"shop/custom-db:1", []string{"MARIADB_VERSION=1:11.4"}, MariaDB},
		{"shop/custom-db:1", []string{"MYSQL_MAJOR=8.4"}, MySQL},
	} {
		if got := detectEngine(test.image, test.env); got != test.want {
			t.Errorf("detectEngine(%q, %d variables) = %q, want %q", test.image, len(test.env), got, test.want)
		}
	}
}

func TestCommandsKeepThePasswordOutOfTheArguments(t *testing.T) {
	const secret = "s3cret-Passw0rd"
	for _, test := range []struct {
		name     string
		engine   string
		env      []string
		dump     []string
		restore  []string
		firstArg string
		userArg  string
		wantEnv  string
	}{
		{"postgres with a user", Postgres, []string{"POSTGRES_USER=shop", "POSTGRES_PASSWORD=" + secret},
			[]string{"pg_dumpall"}, []string{"psql"}, "pg_dumpall", "--username=shop", "PGPASSWORD=" + secret},
		{"postgres defaults", Postgres, nil, []string{"pg_dumpall"}, []string{"psql"}, "pg_dumpall", "--username=postgres", ""},
		{"mysql root", MySQL, []string{"MYSQL_ROOT_PASSWORD=" + secret, "MYSQL_USER=app", "MYSQL_PASSWORD=other"},
			[]string{"mysqldump", "mariadb-dump"}, []string{"mysql", "mariadb"}, "mysqldump", "--user=root", "MYSQL_PWD=" + secret},
		{"mariadb root", MariaDB, []string{"MARIADB_ROOT_PASSWORD=" + secret},
			[]string{"mariadb-dump", "mysqldump"}, []string{"mariadb", "mysql"}, "mariadb-dump", "--user=root", "MYSQL_PWD=" + secret},
		{"mysql application account", MySQL, []string{"MYSQL_USER=app", "MYSQL_PASSWORD=" + secret},
			[]string{"mysqldump", "mariadb-dump"}, []string{"mysql", "mariadb"}, "mysqldump", "--user=app", "MYSQL_PWD=" + secret},
	} {
		t.Run(test.name, func(t *testing.T) {
			dumps, err := dumpCommands(test.engine, test.env)
			if err != nil {
				t.Fatal(err)
			}
			restores, err := restoreCommands(test.engine, test.env)
			if err != nil {
				t.Fatal(err)
			}
			tools := func(commands []command) (out []string) {
				for _, c := range commands {
					out = append(out, c.argv[0])
				}
				return out
			}
			if got := strings.Join(tools(dumps), ","); got != strings.Join(test.dump, ",") {
				t.Errorf("dump tools = %s", got)
			}
			if got := strings.Join(tools(restores), ","); got != strings.Join(test.restore, ",") {
				t.Errorf("restore tools = %s", got)
			}
			for _, c := range append(dumps, restores...) {
				joined := strings.Join(c.argv, " ")
				if strings.Contains(joined, secret) || strings.Contains(joined, "other") {
					t.Errorf("password in the arguments: %s", joined)
				}
				if c.argv[1] != test.userArg {
					t.Errorf("%s: user argument %q, want %q", c.argv[0], c.argv[1], test.userArg)
				}
				if got := strings.Join(c.env, ","); got != test.wantEnv {
					t.Errorf("%s: env %q, want %q", c.argv[0], got, test.wantEnv)
				}
			}
			if test.engine != Postgres {
				joined := strings.Join(dumps[0].argv, " ")
				if !strings.Contains(joined, "--all-databases") || !strings.Contains(joined, "--single-transaction") {
					t.Errorf("dump arguments: %s", joined)
				}
			}
		})
	}
	// A user is one argument, glued to its option: it can never be read as
	// another option, and control characters are refused.
	commands, err := dumpCommands(Postgres, []string{"POSTGRES_USER=--file=/tmp/x"})
	if err != nil || commands[0].argv[1] != "--username=--file=/tmp/x" {
		t.Errorf("user starting with dashes: %v %v", commands, err)
	}
	if _, err := dumpCommands(Postgres, []string{"POSTGRES_USER=a\nb"}); err == nil {
		t.Error("a user with a line break was accepted")
	}
}

func TestScrub(t *testing.T) {
	if got := scrub("access denied using hunter22!", "hunter22"); strings.Contains(got, "hunter22") {
		t.Errorf("scrub left the password: %s", got)
	}
	if got := scrub("a b c", "a"); got != "a b c" {
		t.Errorf("scrub of a too short password changed the text: %s", got)
	}
}

func TestIdentifiers(t *testing.T) {
	for name, want := range map[string]bool{
		"shop-db-1": true, "db_1.prod": true, "a": true,
		"": false, ".": false, "..": false, "../etc": false, "a/b": false, ".hidden": false, "-x": false,
		"a b": false, "a\x00b": false, strings.Repeat("a", 129): false, "é": false, `a\b`: false,
	} {
		if ValidName(name) != want {
			t.Errorf("ValidName(%q) = %v", name, !want)
		}
	}
	for id, want := range map[string]bool{
		"20261122T031500Z": true,
		"":                 false, "20261122T031500": false, "20261122T031500Z.sql.gz": false, "../20261122T031500Z": false,
		"2026-11-22T03:15Z": false, "20261122t031500z": false, "20261122T031500Z/": false,
	} {
		if ValidID(id) != want {
			t.Errorf("ValidID(%q) = %v", id, !want)
		}
	}
	s := &store{root: "/srv/backups"}
	for _, bad := range [][2]string{{"..", "20261122T031500Z"}, {"db", "../../etc/passwd"}, {"a/b", "20261122T031500Z"}, {"db", ""}} {
		if dump, record, err := s.paths(bad[0], bad[1]); err == nil {
			t.Errorf("paths(%q, %q) = %s, %s", bad[0], bad[1], dump, record)
		}
	}
	dump, record, err := s.paths("db", "20261122T031500Z")
	if err != nil || dump != "/srv/backups/db/20261122T031500Z.sql.gz" || record != "/srv/backups/db/20261122T031500Z.json" {
		t.Errorf("paths = %s, %s, %v", dump, record, err)
	}
}

func TestSchedule(t *testing.T) {
	loc := time.FixedZone("test", 3600)
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 11, day, hour, minute, 0, 0, loc) }
	daily := Settings{Mode: ModeDaily, Time: "03:15", Keep: 7}
	// 2026-11-22 is a Sunday.
	weekly := Settings{Mode: ModeWeekly, Time: "03:15", Weekday: 3, Keep: 7}
	for _, test := range []struct {
		name           string
		settings       Settings
		now            time.Time
		previous, next time.Time
	}{
		{"daily before the time", daily, at(22, 2, 0), at(21, 3, 15), at(22, 3, 15)},
		{"daily at the time", daily, at(22, 3, 15), at(22, 3, 15), at(23, 3, 15)},
		{"daily after the time", daily, at(22, 12, 0), at(22, 3, 15), at(23, 3, 15)},
		{"weekly on another day", weekly, at(22, 12, 0), at(18, 3, 15), at(25, 3, 15)},
		{"weekly on the day, before", weekly, at(25, 1, 0), at(18, 3, 15), at(25, 3, 15)},
		{"weekly on the day, after", weekly, at(25, 4, 0), at(25, 3, 15), at(32, 3, 15)},
		{"off", Settings{Mode: ModeOff, Time: "03:15", Keep: 7}, at(22, 12, 0), time.Time{}, time.Time{}},
	} {
		if got := test.settings.previous(test.now); !got.Equal(test.previous) {
			t.Errorf("%s: previous = %s, want %s", test.name, got, test.previous)
		}
		if got := test.settings.next(test.now); !got.Equal(test.next) {
			t.Errorf("%s: next = %s, want %s", test.name, got, test.next)
		}
	}
	for _, bad := range []Settings{
		{Mode: "hourly", Time: "03:00", Keep: 7}, {Mode: ModeDaily, Time: "24:00", Keep: 7}, {Mode: ModeDaily, Time: "3:00", Keep: 7},
		{Mode: ModeDaily, Time: "03:60", Keep: 7}, {Mode: ModeDaily, Time: "ab:cd", Keep: 7}, {Mode: ModeWeekly, Time: "03:00", Weekday: 7, Keep: 7},
		{Mode: ModeDaily, Time: "03:00", Keep: 0}, {Mode: ModeDaily, Time: "03:00", Keep: MaxKeep + 1},
	} {
		if bad.validate() == nil {
			t.Errorf("settings accepted: %+v", bad)
		}
	}
}
