import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Logo } from "@/components/brand/Logo";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { onUnauthenticated, ProblemError } from "@/api/client";
import { authGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import { authAccept, authLogin, authSetup } from "@/api/gen/sdk.gen";
import { invitationToken, ReviewerApp } from "@/shell/annotation";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { markSignedIn, markSignedOut } from "./session";

// Everything behind sign-in. First start (no admin password yet) shows the setup screen; without a session the
// sign-in screen; a 401 anywhere later (expired, signed out elsewhere, revoked) brings the sign-in screen back and
// the app returns to the same URL once signed in.

export function AuthGate({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const { data, error, isPending, refetch } = useQuery({ ...authGetOptions(), staleTime: Infinity, retry: 1 });
  useEffect(() => onUnauthenticated(() => markSignedOut(qc)), [qc]);
  const invitation = useInvitation();

  if (isPending || invitation.pending) return <Screen title="Cadence" busy />;
  if (invitation.error) {
    return (
      <Screen title="This invitation does not open a batch" subtitle="Ask the admin of the batch for a new link.">
        <p role="alert" className="text-sm text-destructive">
          {invitation.error}
        </p>
      </Screen>
    );
  }
  if (error || !data) {
    return (
      <Screen title="Cadence">
        <p role="alert" className="text-sm text-destructive">
          The control plane is not reachable: {error instanceof Error ? error.message : "no answer"}
        </p>
        <Button variant="outline" onClick={() => void refetch()}>
          Try again
        </Button>
      </Screen>
    );
  }
  if (data.setupRequired) return <FirstStart />;
  if (!data.actor) return <SignIn />;
  // A reviewer (an invitation to one annotation batch) sees that batch's Annotate view and nothing else.
  if (data.reviewer) return <ReviewerApp scope={data.reviewer} name={data.actor.name} />;
  return children;
}

/**
 * Opens a reviewer's invitation link (/#invitation=cri_…) once: the token becomes a session for its batch (auth.accept),
 * and the fragment leaves the address bar.
 */
function useInvitation(): { pending: boolean; error?: string } {
  const qc = useQueryClient();
  const [state, setState] = useState<{ pending: boolean; error?: string }>(() => ({ pending: !!invitationToken(window.location.hash) }));
  useEffect(() => {
    const token = invitationToken(window.location.hash);
    if (!token) return;
    // The fragment goes first, so a second run of the effect (React's StrictMode) finds no token and redeems nothing.
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
    void (async () => {
      try {
        const { data } = await authAccept({ body: { token }, throwOnError: true });
        markSignedIn(qc, data);
        setState({ pending: false });
      } catch (err) {
        setState({ pending: false, error: problemText(err) });
      }
    })();
  }, [qc]);
  return state;
}

function Screen({ title, subtitle, busy, children }: { title: string; subtitle?: string; busy?: boolean; children?: ReactNode }) {
  return (
    <main className="flex h-full items-center justify-center bg-background p-6 text-foreground" aria-busy={busy || undefined}>
      <section aria-labelledby="auth-title" className="flex w-full max-w-sm flex-col gap-4 rounded-lg border bg-card p-6 text-card-foreground shadow-sm">
        <div className="flex flex-col gap-1">
          <span className="flex items-center gap-1.5 text-xs font-semibold text-muted-foreground">
            <Logo className="size-4" />
            Cadence
          </span>
          <h1 id="auth-title" className="text-lg font-semibold">
            {title}
          </h1>
          {subtitle ? <p className="text-sm text-muted-foreground">{subtitle}</p> : null}
        </div>
        {children}
      </section>
    </main>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1 text-xs">
      <label className="flex flex-col gap-1">
        <span className="text-muted-foreground">{label}</span>
        {children}
      </label>
      {hint ? <span className="text-muted-foreground">{hint}</span> : null}
    </div>
  );
}

function problemText(err: unknown): string {
  if (err instanceof ProblemError) {
    const fields = err.problem.errors?.map((e) => e.message).join("; ");
    return fields || err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

const MIN_PASSWORD = 12;

function FirstStart() {
  const qc = useQueryClient();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password.length < MIN_PASSWORD) return setError(`The password needs at least ${MIN_PASSWORD} characters.`);
    if (password !== confirm) return setError("The two passwords differ.");
    setBusy(true);
    setError(null);
    try {
      const { data } = await authSetup({ body: { username, password }, throwOnError: true });
      markSignedIn(qc, data);
    } catch (err) {
      setError(problemText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Screen
      title="Set up the admin account"
      subtitle="Cadence has one admin. Choose the password you will sign in with; a lost password is reset on the host with cadence admin reset-password."
    >
      <form onSubmit={submit} className="flex flex-col gap-3">
        <Field label="Username">
          <Input name="username" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required pattern="[a-z][a-z0-9._\-]{1,31}" />
        </Field>
        <Field label="Password" hint={`At least ${MIN_PASSWORD} characters.`}>
          <Input name="password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required autoFocus />
        </Field>
        <Field label="Repeat the password">
          <Input name="confirm" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
        </Field>
        {error ? (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        ) : null}
        <Button type="submit" disabled={busy}>
          Create the admin account
        </Button>
      </form>
    </Screen>
  );
}

function SignIn() {
  const qc = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [needCode, setNeedCode] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const { data } = await authLogin({ body: { username, password, ...(needCode && code ? { totpCode: code } : {}) }, throwOnError: true });
      markSignedIn(qc, data);
    } catch (err) {
      if (err instanceof ProblemError && err.slug === "totp-required") {
        setNeedCode(true);
      } else {
        setError(problemText(err));
        if (needCode) setCode("");
      }
    } finally {
      setBusy(false);
    }
  };
  return (
    <Screen title="Sign in">
      <form onSubmit={submit} className="flex flex-col gap-3">
        <Field label="Username">
          <Input name="username" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required autoFocus />
        </Field>
        <Field label="Password">
          <Input name="password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </Field>
        {needCode ? (
          <Field label="Code from your authenticator app">
            <Input
              name="totpCode"
              inputMode="numeric"
              autoComplete="one-time-code"
              pattern="[0-9]{6}"
              maxLength={6}
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
              required
              autoFocus
            />
          </Field>
        ) : null}
        {error ? (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        ) : null}
        <Button type="submit" disabled={busy}>
          Sign in
        </Button>
      </form>
    </Screen>
  );
}
