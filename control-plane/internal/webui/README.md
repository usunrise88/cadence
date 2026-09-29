# internal/webui
Serves the SPA embedded from `dist/` (`//go:embed all:dist`). Only the placeholder `dist/index.html` is committed; `make web` or the Docker build copies the Vite build in. Unknown paths answer `index.html` (client routes), missing `/assets/*` answer 404; `/assets/*` is cached for a year, `index.html` never.
