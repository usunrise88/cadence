import path from "node:path";

// The e2e admin account: created by global-setup.ts on the fresh database of each run.
export const ADMIN_USER = "admin";
export const ADMIN_PASSWORD = "e2e admin password";
/** Signed-in browser state shared by the specs (cookies of the admin's session). */
export const STORAGE_STATE = path.join(import.meta.dirname, "..", "test-results", ".auth", "admin.json");
