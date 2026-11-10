// Package userauth signs a Telegram user account in once and keeps the session in a file.
package userauth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"golang.org/x/term"
)

// ErrNotLoggedIn means no session exists. Run the login command once.
var ErrNotLoggedIn = errors.New("no Telegram user session: run the login command once")

// SessionFile is the session path inside the data directory.
func SessionFile(dir string) string { return filepath.Join(dir, "user.session") }

// NewClient builds a client that keeps its session in the data directory.
func NewClient(apiID int, apiHash, dir string) *telegram.Client {
	return telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: SessionFile(dir)},
	})
}

// Login asks for the phone number, the code and, if set, the 2FA password on the terminal.
func Login(ctx context.Context, apiID int, apiHash, dir string, in *os.File, out io.Writer) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	client := NewClient(apiID, apiHash, dir)
	return client.Run(ctx, func(ctx context.Context) error {
		flow := auth.NewFlow(&terminal{in: in, r: bufio.NewReader(in), out: out}, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return err
		}
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "signed in as %s (id %d)\n", self.Username, self.ID)
		return nil
	})
}

// Authorized reports whether the client holds a valid session.
func Authorized(ctx context.Context, client *telegram.Client) error {
	st, err := client.Auth().Status(ctx)
	if err != nil {
		return err
	}
	if !st.Authorized {
		return ErrNotLoggedIn
	}
	return nil
}

type terminal struct {
	in  *os.File
	r   *bufio.Reader
	out io.Writer
}

func (t *terminal) line(prompt string) (string, error) {
	fmt.Fprint(t.out, prompt)
	s, err := t.r.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (t *terminal) Phone(context.Context) (string, error) {
	return t.line("phone number with country code (+...): ")
}

func (t *terminal) Code(context.Context, *tg.AuthSentCode) (string, error) {
	return t.line("code from Telegram: ")
}

func (t *terminal) Password(context.Context) (string, error) {
	if !term.IsTerminal(int(t.in.Fd())) {
		return t.line("2FA password: ")
	}
	fmt.Fprint(t.out, "2FA password: ")
	b, err := term.ReadPassword(int(t.in.Fd()))
	fmt.Fprintln(t.out)
	return strings.TrimSpace(string(b)), err
}

func (t *terminal) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error { return nil }

func (t *terminal) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("this phone number has no Telegram account")
}
