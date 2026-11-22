# Applications et routes

L’onglet **Applications** de la page **Conteneurs** montre chaque application
(site, API, base de données…) comme un bloc, et dit quels domaines mènent à
quels conteneurs. Il complète la vue en direct décrite dans
[ops.md](ops.md#live-view-pkgdockerui) et suit les mêmes règles : réservé aux
administrateurs (refusé aux comptes `user`), désactivé par
`TMUXATLAS_DOCKER=off`, lecture de l’API Docker par son socket, aucune commande
`docker` ni `docker compose` lancée — sauf par le bloc **Déploiement**, à
activer à part ([deploiement-applications.md](deploiement-applications.md)).

## Ce que montre l’onglet

Atlas regroupe les conteneurs d’après les étiquettes que Docker Compose pose
lui-même sur ce qu’il crée. Aucun fichier `compose.yaml` ni `.env` n’est ouvert.

| Étiquette | Usage |
| --- | --- |
| `com.docker.compose.project` | nom de l’application : une carte par valeur |
| `com.docker.compose.service` | nom du service de chaque conteneur |
| `com.docker.compose.project.working_dir` | dossier affiché sous le nom |
| `com.docker.compose.project.config_files` | fichiers Compose, en infobulle du dossier |
| `com.docker.compose.depends_on` | ordre des actions (voir plus bas) |

Chaque carte donne :

- le nom de l’application et une pastille d’état : **En cours** (tous les
  conteneurs tournent), **Partiel** (certains seulement), **Arrêté** (aucun).
  La pastille passe au rouge si un conteneur est mort, s’est arrêté sur une
  erreur ou est en mauvaise santé ;
- le nombre de services démarrés sur le total ;
- la liste dépliable des conteneurs, dans l’ordre de démarrage : service, nom
  du conteneur, état, santé, image, ports publiés, et un bouton **Journaux**
  qui ouvre le détail habituel du conteneur (journaux, inspection,
  statistiques, terminal) ;
- les boutons **Démarrer**, **Arrêter** et **Redémarrer**.

Les conteneurs lancés hors de Compose sont réunis dans un groupe
**Sans application**, sans action de groupe : ils se pilotent un par un depuis
l’onglet Conteneurs.

Une application dont un service a fini son travail (migration, initialisation)
reste affichée « Partiel » : les étiquettes ne disent pas si un conteneur
arrêté devait tourner.

## Ce que font exactement les actions

Une action d’application est une suite d’actions sur ses conteneurs
**existants**, les mêmes que celles de l’onglet Conteneurs (`POST
/containers/{id}/start`, `stop` ou `restart` de l’API Docker, mêmes délais).
Rien n’est créé, supprimé, reconstruit ni téléchargé.

| Action | Conteneurs touchés | Ordre |
| --- | --- | --- |
| Démarrer | ceux qui ne tournent pas (créés, arrêtés, morts) | dépendances d’abord |
| Arrêter | ceux qui tournent, redémarrent ou sont en pause | dépendances en dernier |
| Redémarrer | ceux qui tournent, redémarrent ou sont en pause | dépendances d’abord |

- **Ordre.** Un service passe après ceux dont il dépend d’après
  `com.docker.compose.depends_on` ; à défaut d’étiquette, l’ordre est celui des
  noms de service, puis des noms de conteneur, donc toujours le même. Une
  dépendance vers un service absent est ignorée, et une boucle de dépendances
  fait retomber sur l’ordre des noms. Atlas n’attend pas qu’une dépendance soit
  « en bonne santé » avant de démarrer la suivante.
- **Démarrer** relance aussi les services ponctuels déjà terminés (une
  migration, par exemple), comme `docker compose start`.
- **Redémarrer** ne touche pas aux conteneurs arrêtés : un service ponctuel
  terminé n’est pas rejoué. C’est plus prudent que `docker compose restart`.
- **Confirmation.** Chaque action demande confirmation et liste, dans l’ordre,
  les conteneurs qu’elle va toucher. Le serveur refait la liste au moment
  d’agir : c’est son rapport qui fait foi.
- **Échec partiel.** Atlas s’arrête au premier conteneur en échec et ne touche
  pas aux suivants. La réponse indique pour chaque conteneur : fait, déjà dans
  cet état, échec (avec le message de Docker) ou non tenté. L’application peut
  donc rester à moitié arrêtée : la carte l’affiche « Partiel », et l’action
  peut être relancée.
- **Une action à la fois** par application : une seconde demande pendant la
  première est refusée (409).
- **Atlas lui-même.** Si le hub tourne dans un conteneur de l’application
  (son nom d’hôte est alors l’identifiant court de ce conteneur), l’arrêt et le
  redémarrage de l’application sont refusés (409) et les boutons désactivés :
  Atlas se couperait au milieu de la suite et laisserait l’application à
  moitié arrêtée sans pouvoir le signaler. Le démarrage reste permis.
- **Journal.** Chaque conteneur touché, épargné ou en échec est journalisé avec
  le compte, l’application et le résultat, comme les actions unitaires.

### Routes de l’API

Sous `/api`, toutes réservées aux administrateurs ; les changements vérifient
aussi `Origin` et le jeton CSRF.

| Route | Réponse |
| --- | --- |
| `GET /docker/stacks` | `stacks` (applications), `standalone` (conteneurs hors Compose), `routes` |
| `POST /docker/stacks/{project}/start` | `{stack, action, ok, results[]}` |
| `POST /docker/stacks/{project}/stop` | idem |
| `POST /docker/stacks/{project}/restart` | idem |

`{project}` doit ressembler à un nom de projet Compose (lettres, chiffres,
`_`, `.`, `-`) et exister dans une liste fraîche de Docker. Une action acceptée
répond 200 même si un conteneur a échoué : `ok` vaut alors `false` et
`results` détaille chaque conteneur (`done`, `unchanged`, `failed`, `skipped`).
Les refus répondent 400 (nom invalide), 404 (application inconnue), 409
(action déjà en cours, application contenant Atlas) ou 503 (vue désactivée).

## Routes (domaines → conteneurs)

La vue **Routes** de l’onglet est en lecture seule. Pour chaque conteneur,
Atlas lit, dans cet ordre :

1. **Étiquettes Traefik** : `traefik.http.routers.<nom>.rule` donne les
   domaines (`Host(...)`, `HostRegexp(...)`) et les chemins (`PathPrefix(...)`,
   `Path(...)`) ; `traefik.http.services.<nom>.loadbalancer.server.port` donne
   le port du conteneur ; `traefik.enable=false` masque le conteneur. Une règle
   `Host(`a`) || Host(`b`)` ou `Host(`a`, `b`)` donne une ligne par domaine.
2. **Étiquettes caddy-docker-proxy**, dans leur forme simple : `caddy` (ou
   `caddy_0`, `caddy_1`…) donne les adresses du site, `caddy.reverse_proxy:
   {{upstreams 8080}}` le port.
3. **À défaut**, les ports publiés du conteneur (`adresse:port` du serveur →
   port du conteneur), présentés comme « Port publié ».

Chaque ligne indique le domaine ou la règle, le conteneur (avec un lien vers
son détail), le port du conteneur et la source de l’information. L’infobulle
de la première colonne montre la valeur brute de l’étiquette.

## Limites

- **Démarrer, arrêter et redémarrer ne mettent rien à jour** : pas de
  `compose up`, `down`, `pull`, `build`, ni de suppression. Un service défini
  dans le fichier Compose mais jamais créé n’apparaît pas. Pour récupérer le
  code depuis git, reconstruire et relancer, voir le bloc **Déploiement** de
  chaque carte, désactivé par défaut :
  [deploiement-applications.md](deploiement-applications.md).
- **Routes détectées par étiquettes seulement.** Un proxy configuré par
  fichier (nginx, Caddyfile, fichier ou API de Traefik, tunnel) n’apparaît
  pas : la vue ne lit aucun fichier de configuration et n’interroge aucun
  proxy. Un tableau vide ne veut donc pas dire qu’aucun domaine n’est servi.
- **Lecture approximative des règles.** Atlas ne vérifie pas que Traefik ou
  Caddy tourne ni qu’il retient la route. Les règles Traefik composées sont
  lues simplement : les alternatives `||` sont séparées, les conditions niées
  (`!Host(...)`) et les autres critères (`Headers`, `Method`…) sont ignorés.
  Sans étiquette de port, ou avec plusieurs services non reliés à leur routeur,
  le port est affiché « — ». Seuls les routeurs HTTP sont lus (pas TCP ni UDP).
  Pour Caddy, les extraits, imports et blocs imbriqués (`handle`,
  `handle_path`) ne sont pas interprétés.
- **Ports publiés** : ils ne sont listés que pour les conteneurs sans étiquette
  de proxy, et ne disent pas si le port est ouvert dans le pare-feu.
- **Pas d’attente de santé** entre deux conteneurs, et pas d’annulation : une
  action interrompue ne remet pas l’application dans son état précédent.
- Les statistiques (CPU, mémoire) restent dans l’onglet Conteneurs.
