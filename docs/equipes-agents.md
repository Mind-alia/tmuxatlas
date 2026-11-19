# Équipes d'agents

La page **Agents** (`/agents`) montre, pour chaque session Claude Code ouverte sous le compte du hub, les sous-agents qu'elle a lancés : une étoile par sous-agent autour de la session, son état, sa dernière action et les trois précédentes.

## D'où viennent les données

Le hub lit, sans rien écrire, ce que Claude Code tient à jour dans `~/.claude` (`pkg/agentteams`) :

- `sessions/<pid>.json` : les sessions ouvertes, leur nom, leur état et leur session tmux ;
- `projects/<projet>/<session>/subagents/agent-<id>.meta.json` : le type, la description et la fin de chaque sous-agent ;
- `projects/<projet>/<session>/subagents/agent-<id>.jsonl` : son journal, dont on tire les appels d'outils.

Seuls les sous-agents actifs depuis moins de 24 heures sont listés, 24 au plus par session.

## États

| État | Sens |
|---|---|
| En cours | pas fini, a écrit dans son journal depuis moins de 3 minutes |
| Silencieux | pas fini, rien écrit depuis plus de 3 minutes : longue commande ou agent bloqué |
| Arrêté | pas fini, rien écrit depuis plus de 30 minutes : très probablement interrompu |
| Terminé | Claude Code l'a marqué fini |

Claude Code ne donne aucun pourcentage d'avancement : la page affiche le nombre d'actions et la durée.

## Limites

- Seules les sessions du compte qui fait tourner le hub sont lues, pas celles des peers.
- Seul Claude Code est lu (pas Codex ni Antigravity).
- En mode comptes, `GET /api/agent-teams` est réservé aux administrateurs : les lignes citent des commandes et des noms de fichiers de toutes les sessions.
