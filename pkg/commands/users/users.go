package users

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/common"
	"github.com/urfave/cli/v3"
)

func bootstrap(_ context.Context, c *cli.Command) error {
	file := c.String("password-hash-file")
	stdin := c.Bool("password-stdin")
	if (file == "") == !stdin {
		return errors.New("indiquez exactement une source de mot de passe")
	}
	var hash []byte
	var err error
	if stdin {
		var password []byte
		password, err = io.ReadAll(io.LimitReader(os.Stdin, 803))
		password = []byte(strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"))
		if err == nil {
			var encoded string
			encoded, err = accounts.HashPassword(string(password))
			hash = []byte(encoded)
		}
	} else {
		hash, err = os.ReadFile(file)
		hash = []byte(strings.TrimSpace(string(hash)))
	}
	if err != nil {
		return fmt.Errorf("lecture du mot de passe : %w", err)
	}
	store, err := accounts.NewStore()
	if err != nil {
		return err
	}
	_, err = store.BootstrapAdmin(c.String("username"), string(hash), c.Bool("force"))
	return err
}

func list(_ context.Context, c *cli.Command) error {
	store, err := accounts.NewStore()
	if err != nil {
		return err
	}
	users, err := store.List()
	if err != nil {
		return err
	}
	for _, user := range users {
		state := "actif"
		switch {
		case user.Disabled:
			state = "désactivé"
		case user.PasswordHash == "":
			state = "invitation en attente"
		}
		fmt.Fprintf(c.Root().Writer, "%s\t%s\t%s\tlinux=%s\thôtes=%s\n", user.Username, user.Role, state, user.UnixUser, strings.Join(user.Hosts, ","))
	}
	return nil
}

func findUser(store *accounts.Store, username string) (accounts.User, error) {
	users, err := store.List()
	if err != nil {
		return accounts.User{}, err
	}
	for _, user := range users {
		if strings.EqualFold(user.Username, username) {
			return user, nil
		}
	}
	return accounts.User{}, accounts.ErrNotFound
}

func printInvitation(c *cli.Command, token string, invitation accounts.Invitation) {
	fmt.Fprintf(c.Root().Writer, "Lien à transmettre (usage unique, valable jusqu'au %s) :\n  <URL publique>/invite#%s\n",
		invitation.ExpiresAt.Local().Format("2006-01-02 15:04"), token)
}

// add creates an account without a password and prints its invitation link.
func add(_ context.Context, c *cli.Command) error {
	store, err := accounts.NewStore()
	if err != nil {
		return err
	}
	user, err := store.CreatePending(c.String("username"), c.String("role"), c.String("unix-user"))
	if err != nil {
		return err
	}
	token, invitation, err := store.CreateInvitation(user.ID, "cli", c.Duration("valid-for"))
	if err != nil {
		return err
	}
	printInvitation(c, token, invitation)
	return nil
}

func invite(_ context.Context, c *cli.Command) error {
	store, err := accounts.NewStore()
	if err != nil {
		return err
	}
	user, err := findUser(store, c.String("username"))
	if err != nil {
		return err
	}
	token, invitation, err := store.CreateInvitation(user.ID, "cli", c.Duration("valid-for"))
	if err != nil {
		return err
	}
	printInvitation(c, token, invitation)
	return nil
}

// setRole runs as the hub's Unix user, outside any TmuxAtlas session: the
// last-administrator guard still applies.
func setRole(_ context.Context, c *cli.Command) error {
	store, err := accounts.NewStore()
	if err != nil {
		return err
	}
	user, err := findUser(store, c.String("username"))
	if err != nil {
		return err
	}
	_, err = store.SetRole(user.ID, "cli", c.String("role"))
	if err == nil {
		fmt.Fprintln(c.Root().Writer, "Rôle modifié. Les requêtes suivantes l'appliquent ; un WebSocket déjà ouvert le garde jusqu'à sa reconnexion (redémarrer le hub pour couper tout de suite).")
	}
	return err
}

func init() {
	username := &cli.StringFlag{Name: "username", Required: true}
	validFor := &cli.DurationFlag{Name: "valid-for", Value: accounts.DefaultInvitationTTL, Usage: "durée de validité du lien (7 jours au plus)"}
	common.RegisterCommand(&cli.Command{
		Name: "users", Usage: "Gérer les comptes",
		Commands: []*cli.Command{{
			Name: "list", Usage: "Lister les comptes, leur rôle et leurs hôtes", Action: list,
		}, {
			Name: "add", Usage: "Créer un compte et afficher son lien d'invitation",
			Flags: []cli.Flag{
				username,
				&cli.StringFlag{Name: "role", Value: accounts.RoleGuest, Usage: "guest ou admin"},
				&cli.StringFlag{Name: "unix-user", Usage: "compte Linux de la personne (documentaire)"},
				validFor,
			},
			Action: add,
		}, {
			Name: "invite", Usage: "Créer un nouveau lien pour choisir le mot de passe",
			Flags: []cli.Flag{username, validFor}, Action: invite,
		}, {
			Name: "set-role", Usage: "Changer le rôle d'un compte (guest ou admin)",
			Flags: []cli.Flag{username, &cli.StringFlag{Name: "role", Required: true}}, Action: setRole,
		}, {
			Name: "bootstrap-admin", Usage: "Initialiser le compte administrateur",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "username", Required: true},
				&cli.StringFlag{Name: "password-hash-file"},
				&cli.BoolFlag{Name: "password-stdin"},
				&cli.BoolFlag{Name: "force"},
			},
			Action: bootstrap,
		}},
	})
}
