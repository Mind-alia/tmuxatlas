# Déployer une application depuis git

Dans l’onglet **Applications** de la page **Conteneurs**, chaque carte a un
bloc **Déploiement**. Pour une application Docker Compose dont le dossier de
travail est dans un dépôt git, il permet de :

- voir où en est le code (branche, commit, retard sur la branche distante) ;
- **déployer** : récupérer les nouveaux commits, reconstruire les images et
  relancer les services ;
- **revenir en arrière** : relancer les services avec les images d’avant le
  dernier déploiement ;
- consulter le **journal** de chaque opération.

Le code est dans `pkg/deploy` ; les routes sont sous
`/api/docker/stacks/{application}/deploy`.

> ⚠️ **Cette fonction n’a été vérifiée que par des tests automatiques** où git
> et Docker sont simulés. Elle n’a encore jamais tourné contre un vrai Docker.
> Avant de s’en servir sur une application qui compte, essaie-la sur une
> application sans enjeu.

## Activation

Désactivée par défaut. Pour l’activer, ajoute au fichier
`~/.config/tmuxatlas/.env` du compte qui fait tourner le hub :

```
TMUXATLAS_DEPLOY=on
```

puis redémarre Atlas. Seules les valeurs `on`, `true`, `1` et `yes` activent ;
toute autre valeur, ou l’absence de la variable, laisse le bloc en **lecture
seule** : l’état git et l’historique restent visibles, les boutons sont
grisés et le bloc rappelle comment activer.

Les réglages de la page Conteneurs s’appliquent en plus, sans exception :

| Condition | Effet |
| --- | --- |
| compte `user`, ou visiteur non connecté | toutes les routes refusées (403 / 401), y compris la lecture |
| requête de modification sans jeton CSRF ou d’une autre origine | refusée (403) |
| `TMUXATLAS_DOCKER=off` | rien n’est servi (503) |
| mode comptes avec un compte `user` actif, sans `TMUXATLAS_DOCKER=on` | rien n’est servi (503), comme pour le reste de la page Conteneurs |
| `git` ou le client `docker` introuvable sur le serveur | état lisible, opérations refusées |
| l’application contient le conteneur d’Atlas lui-même | opérations refusées : Atlas se couperait en cours de route |

Il faut aussi que le hub voie le dossier de travail et les fichiers Compose
de l’application aux chemins que Docker connaît. C’est le cas quand Atlas
tourne directement sur le serveur ; ce n’est pas le cas quand il tourne dans
un conteneur sans ces dossiers montés au même chemin (ni sans `git` et
`docker` dans son image) : le bloc dit alors que le dossier est introuvable.

## Ce que montre le bloc

L’état est lu par deux commandes git en lecture seule
(`git status --porcelain=v2 --branch --untracked-files=no` et `git log -1`),
à l’ouverture du bloc puis toutes les 20 secondes (toutes les 2 secondes
pendant une opération). **Aucun `git fetch` n’est fait pour afficher l’état** :
« 2 commits de retard » compare avec la branche distante telle que le dernier
fetch l’a laissée. Le vrai retard n’est connu qu’au moment du déploiement.

- la branche courante et la branche distante qu’elle suit ;
- le commit courant (identifiant court, sujet, date) ;
- le retard et l’avance sur la branche distante ;
- les fichiers suivis modifiés sans commit, s’il y en a (les fichiers non
  suivis, comme un `.env` ignoré, ne comptent pas) ;
- l’opération en cours et l’historique.

Le bloc est fermé par défaut ; une fois ouvert, il le reste après un
rechargement de la page (préférence gardée dans le navigateur).

## Déployer

Le bouton **Déployer** ouvre une confirmation qui liste les étapes et les
services concernés. Une fois confirmée, l’opération tourne **sur le serveur,
en tâche de fond** : fermer ou recharger la page ne l’interrompt pas, et le
bloc la retrouve à la réouverture.

Les étapes, dans cet ordre, avec arrêt à la première erreur :

| Étape | Ce qui est fait | Délai maximum |
| --- | --- | --- |
| 1. Vérifier le dépôt | refus si le dossier n’est pas dans un dépôt git, si aucune branche n’est sortie (HEAD détachée), si la branche ne suit aucune branche distante, ou si des fichiers suivis ont des modifications non commitées (elles sont nommées) | 1 min |
| 2. Récupérer | `git fetch` | 3 min |
| 3. Avancer la branche | `git merge --ff-only @{upstream}` : avance rapide uniquement, vers la branche distante suivie | 2 min |
| 4. Mémoriser les images | pour chaque conteneur de l’application, l’identifiant de l’image qu’il utilise est noté, et reçoit une étiquette locale `<dépôt>:atlas-rollback-<AAAAMMJJ-HHMMSS>` (`docker image tag`) ; rien n’est poussé | 2 min |
| 5. Construire | `docker compose -p <projet> --project-directory <dossier> [--env-file …] -f <fichiers…> build` | 40 min |
| 6. Relancer | la même commande avec `up -d` | 10 min |
| 7. Vérifier | après 5 secondes, lecture des conteneurs toutes les 4 secondes, pendant 3 minutes au plus (voir ci-dessous) | 3 min |

Ce qui n’est **jamais** fait : `git reset`, `checkout` ou changement de
branche, `pull` avec fusion, `push`, option `--force`, `compose down`,
suppression de conteneur, de volume ou d’image.

Le projet, le dossier, les fichiers Compose et les éventuels fichiers
`--env-file` sont ceux que Compose a lui-même notés dans les étiquettes des
conteneurs au dernier `up`. Atlas n’ouvre aucun de ces fichiers.

**Vérification finale.** Un conteneur est bon s’il tourne et, quand il a un
healthcheck, s’il est « healthy ». Un conteneur arrêté avec le code 0 est
accepté (service ponctuel, comme une tâche de migration). L’étape réussit
quand tous les conteneurs sont bons sur deux lectures de suite. Elle échoue
tout de suite si un conteneur est « unhealthy », arrêté avec un autre code,
en pause ou mort ; elle échoue au bout de 3 minutes si un conteneur n’a
toujours pas démarré, redémarre encore ou n’a pas fini son healthcheck.

**Un seul déploiement à la fois par application**, et **un seul build à la
fois sur le serveur** : si deux applications sont déployées en même temps, la
seconde attend à l’étape « Construire » que la première ait fini la sienne
(l’attente compte dans les 40 minutes).

**Annuler.** Le bouton **Annuler** arrête la commande en cours (signal
d’arrêt, puis arrêt forcé après 10 secondes) et saute les étapes suivantes.
Ce qui est déjà fait n’est pas défait : voir « En cas d’échec ».

## Revenir en arrière

Dans l’historique, le dernier déploiement porte le bouton **Revenir à la
version d’avant** s’il est allé jusqu’à relancer les services (étape 6
tentée), si au moins une de ses images peut être remise sous son nom et s’il
n’a pas déjà été annulé par un retour arrière réussi. Après confirmation :

1. les images actuellement en service sont mémorisées et étiquetées à leur
   tour (on peut donc les retrouver) ;
2. pour chaque image mémorisée par le déploiement, Atlas vérifie que
   l’étiquette `atlas-rollback-…` désigne toujours la même image, puis la
   remet sous le nom que le service attend
   (`docker image tag <dépôt>:atlas-rollback-… <nom d’image du service>`) ;
   si une seule vérification échoue, aucun nom n’est déplacé ;
3. `docker compose … up -d --no-build` : Compose recrée les conteneurs dont
   l’image a changé ;
4. la même vérification finale qu’un déploiement.

### Limites du retour arrière

- **Le code sur le disque ne recule pas.** Le dépôt reste au nouveau commit ;
  seules les images reculent. Tant que tu ne corriges pas, le code sur le
  disque est en avance sur ce qui tourne, et un prochain « Déployer » (ou un
  `docker compose up --build` à la main) reconstruira le commit fautif.
- **Les migrations de base de données ne sont pas annulées**, pas plus que
  toute autre donnée écrite par la nouvelle version (volumes, fichiers,
  caches). Si la nouvelle version a migré le schéma, l’ancienne image peut ne
  plus fonctionner avec.
- **Le fichier Compose ne recule pas non plus.** Si le déploiement a changé
  `compose.yaml` (variables, ports, volumes, nouveau service), `up -d` relance
  l’ancienne image avec la **nouvelle** configuration.
- **Un seul niveau** : on ne revient que sur le dernier déploiement fait par
  Atlas. Les images plus anciennes gardent leur étiquette et peuvent être
  remises à la main.
- **Images désignées par empreinte** (`image: nom@sha256:…`) ou sans nom :
  elles sont mémorisées mais pas remises (un nom par empreinte ne se
  déplace pas) ; le service est laissé tel quel et l’étape le signale.
- **Image supprimée** (par un `docker image prune -a`, par exemple) : le
  retour arrière échoue à l’étape 2, sans rien toucher.
- **Images tirées d’un registre** (`image: postgres:16`) : le nom local est
  remis sur l’ancienne image. Un service avec `pull_policy: always` la
  retéléchargera au prochain `up`.
- Un déploiement fait à la main (hors Atlas) entre-temps n’est pas connu :
  le retour arrière remet les images d’avant le dernier déploiement **fait
  par Atlas**.

## Journal

Chaque déploiement et chaque retour arrière laisse un enregistrement dans le
dossier de données du hub :
`~/.local/share/tmuxatlas/deploy/<application>/<identifiant>.json` (fichiers
en `0600`, dossiers en `0700`). Il contient la date, le compte, l’application,
les commits avant et après, les images d’avant avec leur étiquette, chaque
étape avec ses commandes, sa durée, la fin de sa sortie et son erreur, puis le
résultat : `success`, `failed`, `cancelled` ou `interrupted`.

- Les 40 derniers enregistrements de chaque application sont gardés ; le bloc
  liste les 15 derniers.
- **Sortie bornée** : 32 Kio par étape, en gardant la fin (là où se trouve
  l’erreur d’un build) ; l’état rafraîchi pendant l’opération n’en porte que
  les 6 derniers Kio.
- **Sortie masquée au mieux** : ce qui a la forme `NOM=valeur` est remplacé
  par `NOM=***` quand le nom contient `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`,
  `PASSPHRASE`, `CREDENTIAL` ou `KEY` ; les identifiants dans une URL
  (`https://utilisateur:motdepasse@…`) et les en-têtes `Bearer` / `Basic`
  aussi. **Ce n’est pas une garantie** : un secret affiché sous une autre
  forme (JSON, valeur seule, nom inhabituel) passe. La sortie n’est donc
  envoyée qu’aux administrateurs, et un build ne devrait pas afficher ses
  secrets.
- Les débuts et fins d’opération, et les refus, sont aussi écrits dans le
  journal du hub (compte, application, résultat).

## En cas d’échec

Le bloc montre l’étape en échec, son message et la fin de sa sortie. Ce qu’il
reste à faire dépend de l’étape :

| Étape en échec | État laissé | À faire |
| --- | --- | --- |
| 1. Vérifier le dépôt | rien n’a changé | commiter, remiser (`git stash`) ou annuler les modifications nommées ; sortir une branche qui suit une branche distante |
| 2. Récupérer | rien n’a changé | vérifier l’accès au dépôt distant depuis le compte du hub (`git fetch` dans le dossier). Atlas ne peut répondre à aucune demande de mot de passe : il faut une clé SSH sans phrase de passe ou un identifiant enregistré |
| 3. Avancer la branche | rien n’a changé | la branche locale a des commits que la distante n’a pas : `git status` puis pousser, rebaser ou supprimer ces commits à la main |
| 4. Mémoriser les images | **le code a avancé**, les services tournent encore sur les anciennes images | corriger la cause (Docker injoignable ?) puis relancer « Déployer » |
| 5. Construire | **le code a avancé**, rien n’a été relancé : l’application tourne toujours sur les anciennes images | lire la sortie du build, corriger par un nouveau commit et redéployer. Pour remettre le code à l’ancien commit en attendant : `git reset --hard <commit d’avant>` à la main (Atlas ne le fait jamais) |
| 6. Relancer | indéterminé : certains conteneurs peuvent avoir été recréés, d’autres non | `docker compose ps` dans le dossier ; relancer « Déployer », ou « Revenir à la version d’avant » |
| 7. Vérifier | les services ont été relancés mais l’un d’eux ne tient pas | lire ses journaux (bouton **Journaux** de la carte), puis corriger et redéployer, ou « Revenir à la version d’avant » |

Autres cas :

- **Rien n’est annulé automatiquement.** Après un échec aux étapes 6 ou 7,
  c’est à toi de choisir entre corriger et revenir en arrière.
- **Annulation ou délai dépassé pendant « Relancer »** : même état
  indéterminé que ci-dessus.
- **Atlas redémarre pendant une opération** : l’enregistrement est marqué
  « interrompu » au démarrage suivant, et son issue est inconnue. Selon la
  façon dont le hub a été arrêté, la commande en cours peut avoir été tuée
  avec lui ou avoir continué seule. Vérifie avec `docker compose ps` et
  `git status` avant de relancer quoi que ce soit.
- **Le retour arrière échoue à l’étape « Relancer »** : les noms d’images
  pointent déjà sur les anciennes images ; `docker compose up -d --no-build`
  à la main dans le dossier finit le travail.
- **Remettre une image à la main** : l’enregistrement donne, pour chaque
  service, l’étiquette de retour arrière ;
  `docker image tag <étiquette> <nom d’image du service>` puis
  `docker compose up -d --no-build`.

## Autres limites

- **Les étiquettes `atlas-rollback-…` ne sont jamais supprimées.** Chaque
  déploiement en ajoute, et les images qu’elles retiennent occupent le
  disque : ni `docker image prune` ni le bouton de nettoyage des images ne
  les enlèvent tant qu’elles sont étiquetées. À nettoyer de temps en temps :
  `docker image ls --filter reference='*:atlas-rollback-*'` puis
  `docker image rm <étiquette>`.
- **`up -d` simple** : pas de `--remove-orphans`, pas de `--profile`. Un
  service lancé avec un profil n’est pas reconstruit ni relancé ; un service
  retiré du fichier Compose reste en place.
- **Pas de `pull`** : une image d’un registre n’est pas rafraîchie, sauf si
  le fichier Compose le demande lui-même (`pull_policy`, `build.pull`).
- **Les hooks git du dépôt s’exécutent** (`post-merge`, par exemple), sous le
  compte du hub, comme pour un `git merge` fait à la main.
- **Sous-modules** : ils ne sont pas mis à jour.
- **Pas de coordination avec les boutons Démarrer / Arrêter / Redémarrer** de
  la carte, ni avec une commande lancée à la main : évite de les utiliser
  pendant un déploiement.
- **Vérification courte** : un service qui plante après les premières
  secondes, sans healthcheck, peut passer la vérification.
- **Qui peut quoi.** Le chemin du dossier et des fichiers Compose vient des
  étiquettes des conteneurs : quiconque peut créer un conteneur sur ce
  serveur peut donc faire construire le dossier de son choix par un
  administrateur qui clique « Déployer ». Créer un conteneur demande déjà
  l’accès à Docker, c’est-à-dire les pleins pouvoirs sur la machine ; le
  déploiement n’y ajoute rien, mais ne lance pas un déploiement sur une
  application que tu ne reconnais pas. Ouvrir le bloc suffit à lancer
  `git status` dans ce dossier, avec la configuration git qui s’y trouve. Les chemins sont vérifiés (absolus,
  normalisés, existants, fichiers ordinaires) et passés en arguments
  séparés, jamais à un shell.
