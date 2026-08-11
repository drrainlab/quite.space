package main

// `terminal passcode` — bind, inspect, reveal and forget the short code
// that stands in for the passphrase on this device.
//
// The passphrase never stops being the real key. Everything here is about
// a shortcut to it: `set` proves the passphrase first and only then seals
// it, `show` hands it back for a backup or a piece of paper, and `forget`
// removes the convenience without touching access.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/drrainlab/quiet_places/kernel/passcode"
	"github.com/drrainlab/quiet_places/node"
)

func runPasscode(args []string) error {
	if len(args) == 0 {
		return passcodeUsage()
	}
	sub, rest := args[0], args[1:]
	flags := parseFlags(rest)
	dataDir := flags["data"]
	if dataDir == "" {
		dataDir = node.DefaultDataDir()
	}

	switch sub {
	case "set":
		return passcodeSet(dataDir, flags)
	case "show":
		return passcodeShow(dataDir, flags)
	case "status":
		return passcodeStatus(dataDir)
	case "forget":
		if err := passcode.Forget(dataDir); err != nil {
			return err
		}
		fmt.Println("the code is gone. The passphrase still opens this device —")
		fmt.Println("nothing was lost but the shortcut.")
		return nil
	default:
		return passcodeUsage()
	}
}

func passcodeUsage() error {
	fmt.Println(`terminal passcode <set|show|status|forget> [--data DIR]

  set      bind a short code to this device's passphrase
             --code 4917            the digits (4-12); asked for if omitted
             --passphrase PHRASE    the existing one; asked for if omitted
             --generate             mint a NEW passphrase, use it, and print it
  show     print the passphrase the code unwraps (for a backup, or paper)
             --code 4917
  status   is a code bound, and how many tries are left
  forget   remove the code; the passphrase still opens everything`)
	return nil
}

// passcodeSet proves the passphrase against the real data directory BEFORE
// sealing it. Without that check a typo would be sealed happily and the
// code would then "work" while opening nothing — the failure would surface
// at the next launch, with no way left to tell which of the two was wrong.
func passcodeSet(dataDir string, flags map[string]string) error {
	code := flags["code"]
	if code == "" {
		var err error
		if code, err = ask("code (4-12 digits): "); err != nil {
			return err
		}
	}

	var phrase string
	if flags["generate"] != "" {
		gen, err := passcode.Generate()
		if err != nil {
			return err
		}
		// A generated passphrase only exists once the keystore accepts it,
		// and that only happens on a directory being created. On an existing
		// one, changing the passphrase is a different operation with its own
		// re-encryption, and pretending otherwise here would lock somebody
		// out of their own history.
		if err := node.VerifyPassphrase(dataDir, []byte(gen)); err != nil {
			return fmt.Errorf("--generate can only bind a passphrase this data "+
				"directory already accepts. This one does not: %w.\n"+
				"Use `terminal passcode set --passphrase <existing>` instead", err)
		}
		phrase = gen
	} else {
		phrase = flags["passphrase"]
		if phrase == "" {
			phrase = os.Getenv("QP_PASSPHRASE")
		}
		if phrase == "" {
			var err error
			if phrase, err = ask("passphrase: "); err != nil {
				return err
			}
		}
		if err := node.VerifyPassphrase(dataDir, []byte(phrase)); err != nil {
			if errors.Is(err, node.ErrWrongPassphrase) {
				return errors.New("that passphrase does not open this data directory — " +
					"nothing was changed")
			}
			return err
		}
	}

	if err := passcode.Bind(dataDir, code, []byte(phrase)); err != nil {
		return err
	}
	fmt.Printf("bound · %d digits · %d tries before it erases itself\n",
		len(code), passcode.MaxAttempts)
	fmt.Println()
	fmt.Println("  Four digits keep out somebody who picked up this device. They are")
	fmt.Println("  not a password: a copy of passcode.json falls to guessing in about")
	fmt.Println("  an hour. The passphrase is still the real key, and it is what a")
	fmt.Println("  backup is encrypted with.")
	if flags["generate"] != "" {
		fmt.Println()
		fmt.Println("  passphrase:", phrase)
		fmt.Println("  Write it down. Nobody — including us — can reset it.")
	}
	return nil
}

func passcodeShow(dataDir string, flags map[string]string) error {
	code := flags["code"]
	if code == "" {
		var err error
		if code, err = ask("code: "); err != nil {
			return err
		}
	}
	phrase, err := passcode.Unwrap(dataDir, code)
	if err != nil {
		return err
	}
	fmt.Println(string(phrase))
	return nil
}

func passcodeStatus(dataDir string) error {
	st, err := passcode.Info(dataDir)
	if err != nil {
		return err
	}
	if !st.Bound {
		fmt.Println("no code is bound on this device — it opens with the passphrase.")
		return nil
	}
	fmt.Printf("bound · %d digits · %d of %d tries left · since %s\n",
		st.Digits, st.AttemptsLeft, passcode.MaxAttempts,
		st.CreatedAt.Format("2006-01-02"))
	return nil
}

// ask reads one line. Deliberately NOT hidden input: this runs on a
// developer's terminal, the value is echoed by every other flag in this
// CLI anyway, and a false sense of secrecy is worse than none. The
// interface people actually use is the lock screen.
func ask(prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
