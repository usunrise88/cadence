import { CheckCircle } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { ApprovalsEmpty, ApprovalsPanel } from "./ApprovalsPanel";

const manifest: PanelManifest = {
  id: "approvals",
  kind: "tool",
  title: "Approvals",
  icon: CheckCircle,
  singleton: true,
  defaultSize: { w: 400, h: 520 },
  defaultLocation: "right",
  help: "panels.approvals",
  commands: ["approvals.approve", "approvals.deny"],
  empty: ApprovalsEmpty,
  component: ApprovalsPanel,
};
export default manifest;
