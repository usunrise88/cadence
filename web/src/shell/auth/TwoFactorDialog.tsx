import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ProblemError } from "@/api/client";
import { authGetOptions, authGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import { totpConfirm, totpDisable, totpEnroll } from "@/api/gen/sdk.gen";
import type { AuthStatus, TotpEnrollment } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { notify } from "@/shell/notifications/store";
import { QrCode } from "./QrCode";

// Turn the signed-in user's TOTP second factor on (enroll → confirm with a code) or off (a current code).

/** A base32 key in groups of four, easier to read and type. */
function groups(secret: string): string {
  return secret.replace(/(.{4})/g, "$1 ").trim();
}

function message(err: unknown): string {
  if (err instanceof ProblemError) return err.problem.detail ?? err.problem.title;
  return err instanceof Error ? err.message : String(err);
}

export function TwoFactorDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const { data } = useQuery({ ...authGetOptions(), staleTime: Infinity });
  const [enrollment, setEnrollment] = useState<TotpEnrollment | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const enabled = !!data?.totpEnabled;

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };
  const updated = (status: AuthStatus, title: string) => {
    qc.setQueryData(authGetQueryKey(), status);
    notify({ level: "success", title });
    onClose();
  };
  const start = () =>
    run(async () => {
      const { data: e } = await totpEnroll({ throwOnError: true });
      setEnrollment(e);
    });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void run(async () => {
      if (enabled) {
        const { data: s } = await totpDisable({ body: { code }, throwOnError: true });
        updated(s, "Two-factor authentication is off");
      } else {
        const { data: s } = await totpConfirm({ body: { code }, throwOnError: true });
        updated(s, "Two-factor authentication is on");
      }
    });
  };

  const codeField = (
    <label className="flex flex-col gap-1 text-xs">
      <span className="text-muted-foreground">Current six-digit code</span>
      <Input
        name="code"
        inputMode="numeric"
        autoComplete="one-time-code"
        pattern="[0-9]{6}"
        maxLength={6}
        value={code}
        onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
        required
        autoFocus
      />
    </label>
  );

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-3">
          <DialogHeader>
            <DialogTitle>Two-factor authentication</DialogTitle>
            <DialogDescription>
              {enabled
                ? "On: signing in asks for a code from your authenticator app. Enter a current code to turn it off."
                : "Off: signing in asks for the password only. Turn it on to also ask for a code from an authenticator app (TOTP)."}
            </DialogDescription>
          </DialogHeader>
          {!enabled && enrollment ? (
            <div className="flex flex-col gap-3 text-xs">
              <ol className="flex list-decimal flex-col gap-1 pl-4 text-muted-foreground">
                <li>Open your authenticator app (Google Authenticator, Microsoft Authenticator, 1Password, Aegis, …) on your phone.</li>
                <li>Add an account and scan this code from the screen.</li>
                <li>Enter the six-digit code the app shows.</li>
              </ol>
              <div className="flex justify-center">
                <QrCode text={enrollment.uri} label="QR code with the two-factor key for your authenticator app" />
              </div>
              <details className="rounded-md border px-2 py-1.5">
                <summary className="cursor-pointer text-muted-foreground">Can’t scan? Type the key instead</summary>
                <div className="mt-2 flex flex-col gap-1.5">
                  <div className="flex items-center gap-2">
                    <code data-testid="totp-secret" className="flex-1 rounded-md border bg-muted px-2 py-1 font-mono text-[13px] tracking-wide break-all select-all">
                      {groups(enrollment.secret)}
                    </code>
                    <Button type="button" size="xs" variant="outline" onClick={() => void navigator.clipboard?.writeText(enrollment.secret)}>
                      Copy
                    </Button>
                  </div>
                  <span className="text-muted-foreground">Time-based, 6 digits, every 30 seconds. Spaces don’t matter.</span>
                </div>
              </details>
              <p className="text-muted-foreground">
                The key is shown only here and now. Keep it out of chats and e-mail: scan it from this screen instead.
              </p>
            </div>
          ) : null}
          {enabled || enrollment ? codeField : null}
          {error ? (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            {enabled ? (
              <Button type="submit" variant="destructive" disabled={busy || code.length !== 6}>
                Turn off
              </Button>
            ) : enrollment ? (
              <Button type="submit" disabled={busy || code.length !== 6}>
                Confirm and turn on
              </Button>
            ) : (
              <Button type="button" disabled={busy} onClick={() => void start()}>
                Set up
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
