package app

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/db"
)

// :delete-account removes your account on synq-server for good (nodes,
// posts, sessions, files, synapses you own; the username is freed) and
// signs this device out of it. The identity - your keys - stays, so
// :register can claim a username again.

type accountDeletedMsg struct {
	username     string
	err          error
	unauthorized bool
}

// deleteAccountCommand implements `:delete-account <username> confirm`.
func (m *Model) deleteAccountCommand(args []string) (string, tea.Cmd) {
	if m.identity == nil {
		return "Create an identity first. Restart Synq and choose \"Create your identity.\"", nil
	}
	if m.apiClient == nil {
		return "There's no synq-server configured, so there is no account to delete.", nil
	}
	if m.username == "" {
		return "You haven't registered a username, so there is no account to delete.", nil
	}
	if m.accessToken == "" {
		return "Log in first (:login), then run :delete-account.", nil
	}
	if len(args) == 0 {
		return fmt.Sprintf("This deletes your account on synq-server for good: your nodes, posts, files and any synapses you own are removed, and the name %s becomes free for anyone to claim. Your keys stay on this device. Type :delete-account %s confirm to proceed.", m.username, m.username), nil
	}
	if args[0] != m.username {
		return fmt.Sprintf("Type your own username to confirm: :delete-account %s confirm", m.username), nil
	}
	if len(args) < 2 || args[1] != "confirm" {
		return fmt.Sprintf("Run :delete-account %s confirm to delete it. There is no undo.", m.username), nil
	}
	client, token, username := m.apiClient, m.accessToken, m.username
	return "Deleting your account...", func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		err := client.DeleteAccount(ctx, token, username)
		return accountDeletedMsg{username: username, err: err, unauthorized: api.IsUnauthorized(err)}
	}
}

func (m Model) handleAccountDeleted(msg accountDeletedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if msg.unauthorized {
			m.commandMsg = "Your session expired - reconnecting. Run :delete-account again in a moment."
			return m, m.maybeReauthCmd()
		}
		m.commandMsg = "Couldn't delete the account: " + sanitizeForTerminal(msg.err.Error())
		return m, nil
	}

	// Sign out locally the same way :logout does (connection, node list,
	// synapses, files, draft state), then forget the username: it is no
	// longer ours.
	next, cmd := m.handleLogoutResult(logoutResultMsg{})
	m = next.(Model)
	m.username = ""
	if m.store != nil {
		// Best effort: the account is already gone either way.
		if err := m.store.SavePreference(db.PrefUsername, ""); err != nil {
			m.commandMsg = fmt.Sprintf("Account %s deleted, but this device still remembers the name: %v", msg.username, err)
			return m, cmd
		}
	}
	m.commandMsg = fmt.Sprintf("Account %s deleted. The name is free again; :register <username> claims one.", msg.username)
	return m, cmd
}
