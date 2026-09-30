package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/backups"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/notify"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Stream O (phase 2): notification rules and settings, the Telegram bot, backups. Everything here is the admin's:
// instance-wide configuration that no project-scoped credential reaches.

func (c commandResponse) VisitNotificationRulesEditResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitNotificationSettingsEditResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitTelegramBotSetResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitTelegramBotVerifyResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitBackupsNewResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitBackupsVerifyResponse(w http.ResponseWriter) error { return c.write(w) }

// ---------------------------------------------------------------- notification rules

// NotificationRulesList implements notificationRules.list.
func (s *Server) NotificationRulesList(ctx context.Context, _ api.NotificationRulesListRequestObject) (api.NotificationRulesListResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rules, err := notify.Rules(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	out := api.NotificationRuleList{Items: make([]api.NotificationRule, 0, len(rules))}
	for _, r := range rules {
		v, err := convert[api.NotificationRule](r.JSON())
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return api.NotificationRulesList200JSONResponse(out), nil
}

// NotificationRulesEdit implements notificationRules.edit.
func (s *Server) NotificationRulesEdit(ctx context.Context, req api.NotificationRulesEditRequestObject) (api.NotificationRulesEditResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	var in notify.RuleEdit
	if c := req.Body.Channels; c != nil {
		in.InApp, in.Telegram = c.InApp, c.Telegram
	}
	if t := req.Body.Timing; t != nil {
		v := string(*t)
		in.Timing = &v
	}
	return s.run(ctx, command(ctx, "notificationRules.edit", req.Params.IdempotencyKey, req.Params.DryRun),
		func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
			r, drafts, err := notify.EditRule(ctx, tx, req.Id, rev, in)
			if err != nil {
				return commands.Result{}, nil, err
			}
			return commands.Result{Status: http.StatusOK, Body: r.JSON(), ETag: commands.ETag(r.Rev)}, drafts, nil
		})
}

// ---------------------------------------------------------------- notification settings and the bot

// notificationSettings renders the settings with the bot's status.
func (s *Server) notificationSettings(ctx context.Context, q storage.Querier, st notify.Settings) (api.NotificationSettings, error) {
	env, err := notify.LoadEnv(ctx, q, s.defaultsDoc())
	if err != nil {
		return api.NotificationSettings{}, err
	}
	tokenSet, err := notify.TokenStored(ctx, q)
	if err != nil {
		return api.NotificationSettings{}, err
	}
	out := api.NotificationSettings{
		Rev: st.Rev, UpdatedAt: st.UpdatedAt, Timezone: env.Location.String(), DigestTime: st.DigestTime,
		QuietHours: api.QuietHours{Enabled: st.QuietHours.Enabled, Start: st.QuietHours.Start, End: st.QuietHours.End},
		Telegram: api.TelegramStatus{
			TokenSet: tokenSet, Polling: s.Poller != nil && s.Poller.Polling(), LastSentAt: st.LastSentAt,
			BotUsername: optional(st.BotUsername), LastError: optional(st.LastError),
			Chats: make([]api.TelegramChat, 0, len(st.Chats)), PendingChats: make([]api.TelegramChat, 0, len(st.Seen)),
		},
	}
	for _, id := range st.Chats {
		out.Telegram.Chats = append(out.Telegram.Chats, api.TelegramChat{Id: id})
	}
	for _, c := range st.Seen {
		out.Telegram.PendingChats = append(out.Telegram.PendingChats, api.TelegramChat{Id: c.ID, Title: optional(c.Title), SeenAt: c.SeenAt})
	}
	return out, nil
}

// NotificationSettingsGet implements notificationSettings.get.
func (s *Server) NotificationSettingsGet(ctx context.Context, _ api.NotificationSettingsGetRequestObject) (api.NotificationSettingsGetResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	st, err := notify.LoadSettings(ctx, s.Pool, s.defaultsDoc())
	if err != nil {
		return nil, err
	}
	v, err := s.notificationSettings(ctx, s.Pool, st)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(st.Rev)
	return api.NotificationSettingsGet200JSONResponse{Body: v, Headers: api.NotificationSettingsGet200ResponseHeaders{ETag: &etag}}, nil
}

// NotificationSettingsEdit implements notificationSettings.edit.
func (s *Server) NotificationSettingsEdit(ctx context.Context, req api.NotificationSettingsEditRequestObject) (api.NotificationSettingsEditResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	in := notify.SettingsEdit{DigestTime: req.Body.DigestTime, Chats: req.Body.TelegramChats}
	if q := req.Body.QuietHours; q != nil {
		in.QuietEnabled, in.QuietStart, in.QuietEnd = q.Enabled, q.Start, q.End
	}
	return s.run(ctx, command(ctx, "notificationSettings.edit", req.Params.IdempotencyKey, req.Params.DryRun),
		func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
			st, drafts, err := notify.EditSettings(ctx, tx, rev, in, s.defaultsDoc())
			if err != nil {
				return commands.Result{}, nil, err
			}
			v, err := s.notificationSettings(ctx, tx, st)
			if err != nil {
				return commands.Result{}, nil, err
			}
			return commands.Result{Status: http.StatusOK, Body: v, ETag: commands.ETag(st.Rev)}, drafts, nil
		})
}

// TelegramBotSet implements telegramBot.set: the token goes into the secret store (telegram-bot-token); the
// settings' revision moves so every open Settings panel re-reads the bot status.
func (s *Server) TelegramBotSet(ctx context.Context, req api.TelegramBotSetRequestObject) (api.TelegramBotSetResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	if s.Secrets == nil {
		return nil, errors.New("telegramBot.set: no secret store configured")
	}
	rev := 0
	if req.Params.IfMatch != nil {
		r, err := commands.ParseIfMatch(*req.Params.IfMatch)
		if err != nil {
			return nil, err
		}
		rev = r
	}
	value := []byte(deref(req.Body.Token))
	if len(value) == 0 {
		return nil, problems.Validation([]problems.FieldError{{Path: "/token", Message: "required"}})
	}
	cmd := command(ctx, "telegramBot.set", req.Params.IdempotencyKey, req.Params.DryRun)
	cmd.RequestHash = commands.HashRequest(http.MethodPut, "/telegram-bot", nil, deref(req.Params.IfMatch),
		[]byte(`{"tokenMac":"`+s.Secrets.RequestMAC(value)+`"}`))
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		cur, err := notify.LoadSettings(ctx, tx, s.defaultsDoc())
		if err != nil {
			return commands.Result{}, nil, err
		}
		exists, err := notify.TokenStored(ctx, tx)
		if err != nil {
			return commands.Result{}, nil, err
		}
		switch {
		case exists && rev == 0:
			return commands.Result{}, nil, commands.Precondition("telegram bot token")
		case rev != 0:
			if err := commands.CheckRev(notify.SettingsKind, rev, cur.Rev); err != nil {
				return commands.Result{}, nil, err
			}
		}
		_, _, drafts, err := s.Secrets.Set(ctx, tx, notify.TokenSecret, "telegram", value, cmd.Actor, cmd.DryRun)
		if err != nil {
			return commands.Result{}, nil, err
		}
		st, more, err := notify.BumpSettings(ctx, tx, s.defaultsDoc())
		if err != nil {
			return commands.Result{}, nil, err
		}
		v, err := s.notificationSettings(ctx, tx, st)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: v, ETag: commands.ETag(st.Rev)}, append(drafts, more...), nil
	})
}

// TelegramBotVerify implements telegramBot.verify: getMe with the stored token and, unless it is a dry run, a test
// message to every allow-listed chat.
func (s *Server) TelegramBotVerify(ctx context.Context, req api.TelegramBotVerifyRequestObject) (api.TelegramBotVerifyResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	cmd := command(ctx, "telegramBot.verify", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd,
		func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
			st, err := notify.LoadSettings(ctx, tx, s.defaultsDoc())
			if err != nil {
				return commands.Result{}, nil, err
			}
			out := api.TelegramBotVerify{Chats: []struct {
				Delivered bool    `json:"delivered"`
				Error     *string `json:"error,omitempty"`
				Id        int64   `json:"id"`
			}{}}
			result := func() (commands.Result, []events.Draft, error) {
				return commands.Result{Status: http.StatusOK, Body: out}, []events.Draft{{
					Topic: events.EntityTopic(notify.SettingsKind, notify.SettingsID), Type: "telegram_bot.verified",
					Entity:  &events.EntityRef{Kind: notify.SettingsKind, ID: notify.SettingsID, Rev: st.Rev},
					Payload: map[string]any{"ok": out.Ok, "botUsername": out.BotUsername},
				}}, nil
			}
			exists, err := notify.TokenStored(ctx, tx)
			if err != nil {
				return commands.Result{}, nil, err
			}
			if !exists {
				msg := "no bot token is stored; set one first"
				out.Error = &msg
				return result()
			}
			client, err := s.telegram().Client(ctx)
			if err != nil {
				return commands.Result{}, nil, err
			}
			me, err := client.GetMe(ctx)
			if err != nil {
				msg := err.Error()
				out.Error = &msg
				_ = notify.RecordBot(ctx, tx, "", err, false)
				return result()
			}
			out.BotUsername = &me.Username
			_ = notify.RecordBot(ctx, tx, me.Username, nil, false)
			out.Ok = len(st.Chats) > 0
			if len(st.Chats) == 0 {
				msg := "the token works, but no chat is allow-listed: add a chat id"
				out.Error = &msg
			}
			if cmd.DryRun {
				return result()
			}
			for _, chat := range st.Chats {
				item := struct {
					Delivered bool    `json:"delivered"`
					Error     *string `json:"error,omitempty"`
					Id        int64   `json:"id"`
				}{Id: chat}
				if _, err := client.SendMessage(ctx, chat, "Cadence test message: notifications reach this chat.", nil); err != nil {
					msg := err.Error()
					item.Error = &msg
					out.Ok = false
				} else {
					item.Delivered = true
				}
				out.Chats = append(out.Chats, item)
			}
			_ = notify.RecordBot(ctx, tx, me.Username, nil, true)
			return result()
		})
}

// telegram is the bot configuration with the server's secret store.
func (s *Server) telegram() notify.Bot {
	b := s.Telegram
	if b.Secrets == nil && s.Secrets != nil {
		b.Secrets = s.Secrets
	}
	return b
}

// DecideApproval implements notify.Decider: a verified button press decides the approval as the admin, with
// channel telegram, through approvals.approve or approvals.deny — the same command, audit row and event as a
// decision in the UI.
func (s *Server) DecideApproval(ctx context.Context, approvalID string, approve bool, by notify.Presser) (string, error) {
	a, err := approvals.Get(ctx, s.Pool, approvalID)
	if err != nil {
		return "", err
	}
	if a.State != approvals.StatePending {
		return a.State, notify.ErrAlreadyDecided
	}
	var name string
	if err := s.Pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, credentials.AdminID).Scan(&name); err != nil {
		return "", fmt.Errorf("read the admin account: %w", err)
	}
	actor := auth.Actor{Kind: auth.KindUser, ID: credentials.AdminID, Name: name, Channel: "telegram"}
	ctx = auth.WithActor(ctx, actor)
	ctx = auth.WithScope(ctx, auth.FullScope())
	ctx = policy.WithScope(ctx, policy.Scope{RegistryRead: true})
	note := "decided from Telegram"
	if by.Username != "" {
		note += " (@" + by.Username + ")"
	}
	verb := "deny"
	if approve {
		verb = "approve"
	}
	key := "telegram:" + approvalID + ":" + verb
	ifMatch := strconv.Itoa(a.Rev)
	if approve {
		_, err = s.ApprovalsApprove(ctx, api.ApprovalsApproveRequestObject{Id: approvalID,
			Params: api.ApprovalsApproveParams{IdempotencyKey: key, IfMatch: ifMatch}, Body: &api.ApprovalApprove{Note: &note}})
	} else {
		_, err = s.ApprovalsDeny(ctx, api.ApprovalsDenyRequestObject{Id: approvalID,
			Params: api.ApprovalsDenyParams{IdempotencyKey: key, IfMatch: ifMatch}, Body: &api.ApprovalDeny{Note: &note}})
	}
	var pe *problems.Error
	if errors.As(err, &pe) && (pe.Type == problems.Conflict || pe.Type == problems.PreconditionFailed) {
		cur, gerr := approvals.Get(ctx, s.Pool, approvalID)
		if gerr == nil && cur.State != approvals.StatePending {
			return cur.State, notify.ErrAlreadyDecided
		}
	}
	if err != nil {
		return "", err
	}
	cur, err := approvals.Get(ctx, s.Pool, approvalID)
	if err != nil {
		return "", err
	}
	return cur.State, nil
}

// ---------------------------------------------------------------- backups

func (s *Server) backupsService() (*backups.Service, error) {
	if s.Backups == nil {
		return nil, errors.New("backups are not configured")
	}
	return s.Backups, nil
}

// BackupsList implements backups.list.
func (s *Server) BackupsList(ctx context.Context, req api.BackupsListRequestObject) (api.BackupsListResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	svc, err := s.backupsService()
	if err != nil {
		return nil, err
	}
	list, err := backups.List(ctx, s.Pool, deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	sc, err := svc.ScheduleOf(ctx, s.Pool, s.defaultsDoc(), time.Now())
	if err != nil {
		return nil, err
	}
	out := api.BackupList{Items: make([]api.Backup, 0, len(list))}
	if out.Schedule, err = convert[api.BackupSchedule](sc); err != nil {
		return nil, err
	}
	for _, b := range list {
		v, err := convert[api.Backup](b)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	if last, ok, err := backups.LastRestoreTest(ctx, s.Pool); err != nil {
		return nil, err
	} else if ok {
		rep, err := convert[api.RestoreTest](last.RestoreTest)
		if err != nil {
			return nil, err
		}
		out.LastRestoreTest = &struct {
			BackupId string          `json:"backupId"`
			Report   api.RestoreTest `json:"report"`
		}{BackupId: last.ID, Report: rep}
	}
	return api.BackupsList200JSONResponse(out), nil
}

// BackupsGet implements backups.get.
func (s *Server) BackupsGet(ctx context.Context, req api.BackupsGetRequestObject) (api.BackupsGetResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	b, err := backups.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	v, err := convert[api.Backup](b)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(b.Rev)
	return api.BackupsGet200JSONResponse{Body: v, Headers: api.BackupsGet200ResponseHeaders{ETag: &etag}}, nil
}

// BackupsNew implements backups.new: a manual set now; 202 {jobId} (a dry run answers the would-be set).
func (s *Server) BackupsNew(ctx context.Context, req api.BackupsNewRequestObject) (api.BackupsNewResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	cmd := command(ctx, "backups.new", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		svc, err := s.backupsService()
		if err != nil {
			return commands.Result{}, nil, err
		}
		b, jobID, drafts, err := svc.Enqueue(ctx, tx, backups.TriggerManual, cmd.Actor, cmd.DryRun)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: b, ETag: commands.ETag(b.Rev)}, nil, nil
		}
		return commands.Result{Status: http.StatusAccepted, Body: api.JobAccepted{JobId: jobID}}, drafts, nil
	})
}

// BackupsVerify implements backups.verify: the restore test of one set, now; 202 {jobId}.
func (s *Server) BackupsVerify(ctx context.Context, req api.BackupsVerifyRequestObject) (api.BackupsVerifyResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "backups.verify", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		svc, err := s.backupsService()
		if err != nil {
			return commands.Result{}, nil, err
		}
		b, jobID, drafts, err := svc.EnqueueRestoreTest(ctx, tx, req.Id, rev, cmd.Actor, cmd.DryRun)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: b, ETag: commands.ETag(b.Rev)}, nil, nil
		}
		return commands.Result{Status: http.StatusAccepted, Body: api.JobAccepted{JobId: jobID}}, drafts, nil
	})
}
