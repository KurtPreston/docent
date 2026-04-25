# Dogfood example userdata

Copy these files into your `userdata/` directory (or merge entries) after `slakkr setup`. Replace `YOUR_HOST` in `projects.yaml` with your `SLAKKR_HOST` value (see `configure_host`).

Paths assume the slakkr-ai repo lives at `/Users/kurt/Code/slakkr-ai`; adjust `paths_by_host` to your machine.

For Gitea, set `SLAKKR_SLAKKR_GITEA_TOKEN` (or the env name from your directive) in `userdata/.env`.

Optional: copy `config.yaml` from this folder to merge Ollama settings into `userdata/config.yaml`.
