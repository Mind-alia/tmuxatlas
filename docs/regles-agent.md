# Règles pour le Claude du compte agents

Ces règles s'appliquent à tout travail sur ce dépôt depuis le compte `agents`.
Elles complètent [`deploiement.md`](deploiement.md) et
[`sudo-terminal.md`](sudo-terminal.md).

## Sur quelle base travailler

- La version en service est la branche `prod/…` la plus récente, et non `main`.
  `main` n'a pas les fonctions de la version perso :
  - l'explorateur et l'éditeur de fichiers ;
  - les sessions qui survivent au redémarrage ;
  - la dictée sur le Wall et dans agy ou Codex, et la touche du micro ;
  - la mise en page pour le téléphone ;
  - le correctif du PTY.
- Pour chaque chantier, crée une branche depuis cette branche `prod/…` et ouvre
  une PR vers elle.
- Ne supprime ni ne réécris une fonction existante sans demande de
  l'utilisateur.
- Avant de proposer une PR, lance :
  - `go vet ./...` ;
  - `go test ./...`, avec `TMUX` vidé et un `TMUX_TMPDIR` temporaire ;
  - `cd web && npm test && npm run build`.
- Dans la PR, dis ce qui a été testé et ce qui ne l'a pas été.

## Ce que l'utilisateur attend

- Il utilise TmuxAtlas par un seul tunnel SSH, sur l'interface du hub, et y
  veut tout trouver.
  - Ne lui demande ni second tunnel, ni instance de test, ni passkey de test.
  - Teste toi-même, puis indique en une ligne où trouver la fonction après le
    déploiement.
- Fais la fonction comme il l'a demandée. Si c'est impossible, dis-le et
  explique pourquoi, au lieu de livrer autre chose.
- L'interface et les messages sont en français, et on tutoie l'utilisateur.

## Déploiement

- Tu ne déploies jamais. Le compte de prod :
  - relit un commit précis, désigné par son SHA complet ;
  - le compile lui-même ;
  - l'installe en gardant l'ancien binaire.
- Rien de ce qui est déployé ne vient d'un fichier que le compte agents peut
  écrire.
- Ne propose jamais `tmuxatlas install --mode hub` pour le hub de prod.
- Garde `KillMode=process` dans l'unité du hub : sans cette ligne, un
  redémarrage tue toutes les sessions tmux de la prod.

## Sécurité

- Le compte agents n'a ni sudo ni Docker, et ne doit pas les obtenir.
- Pour une commande root, dépose une demande :
  `tmuxatlas sudo-request --reason "…" -- "<commande>"`.
  - L'humain la relit, l'approuve et tape lui-même son mot de passe dans le
    terminal sudo.
  - Tu ne reçois que `done`, `refused`, `cancelled` ou `expired`. Vérifie
    toi-même l'effet de la commande.
- Ne demande jamais à l'humain de taper un mot de passe dans un terminal du
  compte agents.
- Ce dépôt est public. Ne mets jamais dans le code, les commits ou les PR :
  - de secret, de jeton ou de clé ;
  - d'adresse IP ;
  - de donnée de la prod.
- Les tests ne touchent jamais la prod :
  - utilise un socket TmuxAtlas et un serveur tmux à part ;
  - n'utilise jamais `--no-auth` sur TCP ;
  - n'agis jamais sur les conteneurs de prod.
