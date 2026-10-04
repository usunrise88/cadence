import { useQuery, useQueryClient } from "@tanstack/react-query";
import { LogOut } from "iconoir-react";
import { authLogout } from "@/api/gen/sdk.gen";
import { batchesGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { ReviewerScope } from "@/api/gen/types.gen";
import { Logo } from "@/components/brand/Logo";
import { Button } from "@/components/ui/button";
import { markSignedOut } from "@/shell/auth/session";
import { errorMessage } from "@/shell/panel/commands";
import { AnnotateView } from "./AnnotateView";

// A reviewer's page (docs/spec/06-platform.md "Authentication and access"; phase 4 · stream A): someone invited to one
// annotation batch sees that batch's Annotate view and nothing else — no workspaces, no Library, no other data. Audio
// plays through short-lived signed links; there is no download.

export function ReviewerApp({ scope, name }: { scope: ReviewerScope; name?: string }) {
  const qc = useQueryClient();
  const batch = useQuery({ ...batchesGetOptions({ path: { id: scope.batchId } }) });
  const b = batch.data;
  const signOut = async () => {
    await authLogout({ throwOnError: false });
    markSignedOut(qc);
  };
  return (
    <div className="flex h-full flex-col bg-background text-foreground" data-slot="reviewer-app">
      <div className="flex h-10 shrink-0 items-center gap-3 border-b px-3 text-xs" role="navigation" aria-label="Reviewer">
        <span className="flex items-center gap-1.5 font-semibold">
          <Logo className="size-4" />
          Cadence
        </span>
        <span className="font-medium">{b ? `Annotation batch ${b.name}` : "Annotation batch"}</span>
        {b ? (
          <span className="text-muted-foreground">
            {b.progress.agreed + b.progress.adjudicated + b.progress.excluded} of {b.progress.items} resolved · guidelines {b.guidelines.path} at {b.guidelines.commit.slice(0, 8)}
          </span>
        ) : null}
        <span className="ml-auto text-muted-foreground">
          {name ? `${name} · ` : ""}
          {scope.role}
          {scope.expiresAt ? ` · access until ${new Date(scope.expiresAt).toLocaleDateString()}` : ""}
        </span>
        <Button size="xs" variant="ghost" onClick={() => void signOut()}>
          <LogOut aria-hidden />
          Sign out
        </Button>
      </div>
      {batch.error ? (
        <p role="alert" className="p-4 text-sm text-destructive">
          {errorMessage(batch.error)}
        </p>
      ) : b && b.state !== "open" && b.state !== "failed" ? (
        <p className="p-6 text-center text-sm text-muted-foreground">This batch is {b.state}: thank you, your work is in.</p>
      ) : (
        <main className="min-h-0 flex-1">
          <AnnotateView batchId={scope.batchId} />
        </main>
      )}
    </div>
  );
}
