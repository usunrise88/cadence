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

// Turn the signed-in user's TOTP second factor on (enroll → confirm with a code) or off (a current code).

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
            <div className="flex flex-col gap-2 text-xs">
              <p className="text-muted-foreground">Add this key to your authenticator app, then enter the code it shows.</p>
              <code data-testid="totp-secret" className="rounded-md border bg-muted px-2 py-1 font-mono text-[13px] break-all select-all">
                {enrollment.secret}
              </code>
              <details>
                <summary className="cursor-pointer text-muted-foreground">Setup link (otpauth)</summary>
                <code className="mt-1 block font-mono break-all select-all">{enrollment.uri}</code>
              </details>
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
