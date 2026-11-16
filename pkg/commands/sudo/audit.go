package sudocmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/LosFurina/tmuxatlas/pkg/common"
	"github.com/LosFurina/tmuxatlas/pkg/sudoterm"
)

func init() {
	common.RegisterCommand(&cli.Command{
		Name:  "sudo-audit",
		Usage: "show the sudo terminal audit log of this account",
		Description: `Prints the last entries of $XDG_STATE_HOME/tmuxatlas/sudo-audit.jsonl,
written by "tmuxatlas agent --sudo-terminal". The log holds the proposed
commands, requesters, approvers, durations and shell exit codes, never
keystrokes or output.`,
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "limit", Value: 50, Usage: "number of entries"},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			path, err := sudoterm.AuditPath()
			if err != nil {
				return err
			}
			file, err := os.Open(path)
			if os.IsNotExist(err) {
				fmt.Println("aucune entrée :", path)
				return nil
			}
			if err != nil {
				return err
			}
			defer file.Close()
			entries, err := sudoterm.ReadAudit(file, int(c.Int("limit")))
			if err != nil {
				return err
			}
			table := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(table, "HEURE\tÉVÉNEMENT\tDEMANDE\tV\tAPPROBATEUR\tORIGINE\tSHELL\tCOMMANDE PROPOSÉE")
			for _, entry := range entries {
				exit := "-"
				if entry.ShellExit != nil {
					exit = fmt.Sprint(*entry.ShellExit)
				}
				fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%s\t%s/%s\t%s\t%q\n",
					entry.Time.Local().Format("2006-01-02 15:04:05"), entry.Event, entry.RequestID, entry.Version,
					entry.Approver, entry.OriginHost, entry.OriginSession, exit, entry.Command)
			}
			return table.Flush()
		},
	})
}
