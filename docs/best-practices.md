# Best practices

Read this before changing docent.

## Docent is public and generic

Docent is a public project. Many of its features start from one person's
private workflow, but docent itself must stay useful to anyone and reveal
nothing about where a feature came from.

- **Build the generic tool, not the use case.** When a feature is motivated by
  a particular organization, repository, team, or workflow, add the general
  capability it needs (a collector option, a trigger field, a template
  variable) and let configuration select the particular case. No code path
  should exist for one specific repository, team, or path.
- **Keep specifics in configuration.** Names, paths, prompts, and policies for
  a particular use belong in the user's own `~/.config/docent/config.yaml`, or
  in a local script a `shell` action runs, never in this repository.
- **Use placeholders everywhere public.** Docs, recipes, tests, examples,
  commit messages, plans, and pull requests use stand-ins such as
  `my-org/my-repo`, `my-team`, and `services/api/`. Never name a private
  organization, repository, team, host, service, or person. Deleted files stay
  in history, so this applies to temporary files such as plans too.

Why: a generic capability serves every user and keeps docent free of special
cases, and keeping private details out protects the people and organizations
whose workflows inspired a feature.
