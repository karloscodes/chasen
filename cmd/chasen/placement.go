package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/karloscodes/chasen/protocol"
	"golang.org/x/term"
)

// placed returns the credentials with the cloud server for a new app. The
// cloud says which servers the account has and what a new one costs, and the
// user picks. Your own server, an app that already has its server, and a run
// without a terminal (CI) need no question. CHASEN_CLOUD names another cloud.
func placed(creds credentials, app string) (credentials, error) {
	if creds.Server != "" || creds.URL != apiAddress("cloud") {
		return creds, nil
	}
	p, ok, err := protocol.Client(creds).Placement(context.Background(), app)
	if errors.Is(err, protocol.ErrUnauthorized) {
		return creds, errUnauthorized
	}
	if err != nil || !ok || p.Server != "" {
		return creds, err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return creds, nil
	}
	creds.Server, err = choosePlacement(app, p, os.Stdin, os.Stdout)
	return creds, err
}

func euros(cents int64) string { return fmt.Sprintf("€%d.%02d", cents/100, cents%100) }

// choosePlacement asks where a new app goes: one of the servers of the
// account, or a new server of one of the types. The first option is the
// default, and it is the cheapest one: a server you already pay for, or the
// cheapest type. The answer has the form of protocol.ServerHeader.
func choosePlacement(app string, p protocol.Placement, in io.Reader, out io.Writer) (string, error) {
	var choices []string
	table := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	for _, s := range p.Servers {
		choices = append(choices, s.ID)
		apps := "no apps yet"
		if len(s.Apps) > 0 {
			apps = "runs " + strings.Join(s.Apps, ", ")
		}
		fmt.Fprintf(table, "  %d\tyour server %s\t%s\t%s\t%s\tno extra cost\n", len(choices), s.ID, s.Region, cmp.Or(s.Type, "your own"), apps)
	}
	if p.CanCreate {
		// The list is cheapest first. Show the four cheapest of each region:
		// --new=<type>@<location> reaches the larger ones.
		shown := map[string]int{}
		for _, t := range p.Types {
			if shown[t.Region]++; shown[t.Region] > 4 {
				continue
			}
			choices = append(choices, "new:"+t.Name+"@"+t.Location)
			fmt.Fprintf(table, "  %d\ta new server\t%s\t%s\t%d vCPU, %d GB memory, %d GB disk\t%s a month at Hetzner\n", len(choices),
				t.Region, t.Name, t.Cores, t.MemoryGB, t.DiskGB, euros(t.MonthlyCents))
		}
	}
	if len(choices) < 2 {
		return "", nil // nothing to choose. The cloud uses the one server, or says why it cannot
	}

	if len(p.Servers) == 0 {
		fmt.Fprintf(out, "%s is your first app. Chasen creates a server for it. Which one?\n\n", app)
	} else {
		fmt.Fprintf(out, "%s is a new app. Where does it go?\n\n", app)
	}
	table.Flush()
	if p.CanCreate {
		fmt.Fprintln(out, "\nPick the region of your customers. A server costs what Hetzner charges at this moment, without VAT.")
		fmt.Fprintf(out, "Chasen is %s a month for your account, with any number of servers.\n", euros(p.FeeCents))
	} else if p.Reason != "" {
		fmt.Fprintf(out, "\nA new server is not possible now: %s\n", p.Reason)
	}

	lines := bufio.NewReader(in)
	for {
		fmt.Fprintf(out, "Choice [1]: ")
		line, err := lines.ReadString('\n')
		answer := strings.TrimSpace(line)
		if answer == "" && err == nil {
			return choices[0], nil
		}
		if n, convErr := strconv.Atoi(answer); convErr == nil && n >= 1 && n <= len(choices) {
			return choices[n-1], nil
		}
		if err != nil {
			return "", errors.New("no choice. Nothing is deployed")
		}
		fmt.Fprintf(out, "Type a number from 1 to %d.\n", len(choices))
	}
}
