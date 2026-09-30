import { useQuery } from "@tanstack/react-query";
import { authGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import { Button } from "@/components/ui/button";
import { useCommand } from "@/shell/panel";
import { Chip, SectionHeading } from "./ui";

// Security: the admin's second factor. Enrolment and disabling live in the chrome's two-factor dialog (the user
// menu opens the same one); this section shows the state and opens it.

export function SecuritySection() {
  const { data } = useQuery({ ...authGetOptions(), staleTime: Infinity });
  const twoFactor = useCommand("view.twoFactor");
  const on = !!data?.totpEnabled;
  return (
    <section aria-labelledby="settings-security" className="flex flex-col gap-3">
      <SectionHeading id="settings-security" title="Security" hint="Sign-in for the admin account. Passwords are reset from the host shell (`cadence admin reset-password`)." />
      <div className="flex flex-wrap items-center gap-2 rounded-md border p-3 text-xs">
        <span className="font-medium">Two-factor authentication (TOTP)</span>
        <Chip tone={on ? "accent" : "warning"}>{on ? "On" : "Off"}</Chip>
        <span className="text-muted-foreground">{on ? "Sign-in asks for a code from your authenticator app." : "Recommended: sign-in then needs a code as well as the password."}</span>
        {twoFactor ? (
          <Button size="xs" variant="outline" className="ml-auto" onClick={() => void twoFactor.run()}>
            {on ? "Turn off…" : "Turn on…"}
          </Button>
        ) : null}
      </div>
    </section>
  );
}
