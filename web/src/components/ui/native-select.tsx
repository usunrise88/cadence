import * as React from "react"
import { cn } from "cn"

// A styled native <select> (shadcn's native-select): the platform's own picker, keyboard and screen-reader support,
// and no portal to manage in popout windows.
function NativeSelect({ className, ...props }: React.ComponentProps<"select">) {
  return (
    <select
      data-slot="native-select"
      className={cn(
        "h-7 w-full min-w-0 rounded-md border border-input bg-background px-1.5 py-1 text-[13px] text-foreground transition-colors outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40 disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive",
        className
      )}
      {...props}
    />
  )
}

export { NativeSelect }
