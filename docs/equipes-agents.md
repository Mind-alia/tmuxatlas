# Équipes d'agents

La page **Agents** (`/agents`) montre, pour chaque session Claude Code ouverte sous le compte du hub, les sous-agents qu'elle a lancés : une étoile par sous-agent autour de la session, son état, sa dernière action et les trois précédentes.

## Dans les listes de sessions

Les sous-agents se voient aussi là où tu regardes tes sessions, sans ouvrir la page :

- **Barre latérale** : sous chaque session qui a des sous-agents en cours, une ligne par sous-agent avec sa description et son action du moment, trois lignes au plus puis « +N ». Rien pour les autres sessions.
- **Carte de la vue d'ensemble et tuile du Mur** : une pastille « N agents » pour les sous-agents en cours ; s'il n'en reste aucun, une pastille discrète « N terminés » pour ceux qui ont fini depuis moins d'une heure.
- **Vue d'une session** : un bouton « Agents » dans l'en-tête du terminal, dès que la session a des sous-agents listés par la page.

Un clic sur l'un des trois ouvre la page sur cette session (`/agents?session=<nom tmux>`). « En cours » y regroupe les états En cours et Silencieux ; un sous-agent silencieux porte un point orange dans la barre latérale.

Ces trois endroits partagent un seul sondage de `GET /api/agent-teams/summary`, toutes les 5 secondes, en pause quand l'onglet est caché. La réponse ne contient aucun journal : par session tmux, le nombre en cours, le nombre de terminés récents, le total, et pour les seuls sous-agents en cours leur identifiant, leur description, leur action et leur état. Les sessions Claude Code d'une même session tmux sont additionnées ; celles qui tournent hors de tmux n'y figurent pas.

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

Il n'y a pas d'état « En échec ». Le fichier `.meta.json` ne porte que `finished`, sans résultat ni code d'erreur, et la dernière ligne du journal ne distingue pas une fin normale d'une fin en erreur. Un sous-agent interrompu reste simplement non fini, puis passe à Silencieux et à Arrêté.

## Limites

- Seules les sessions du compte qui fait tourner le hub sont lues, pas celles des peers.
- Seul Claude Code est lu (pas Codex ni Antigravity).
- En mode comptes, `GET /api/agent-teams` et `GET /api/agent-teams/summary` sont réservés aux administrateurs : les lignes citent des commandes et des noms de fichiers de toutes les sessions. Un compte `user` ne voit donc ni pastille ni liste, et son navigateur ne sonde pas la route.
- Les pastilles et la liste ne concernent que les sessions tmux du hub, pas celles d'un pair qui porterait le même nom.
