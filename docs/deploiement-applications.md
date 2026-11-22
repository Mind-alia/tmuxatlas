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

> **Ce qui a été essayé.** Le chemin nominal — un déploiement puis un retour
> arrière réussis — a été essayé avec Docker Compose v5 sur une application
> jetable. Tout le reste (échecs à chaque étape, annulation, délais, nettoyage
> des étiquettes, images partagées) n’est vérifié que par des tests
> automatiques où git et Docker sont simulés. Avant de t’en servir sur une
> application qui compte, essaie-le sur une application sans enjeu.

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
| 4. Mémoriser les images | pour chaque conteneur de l’application, l’identifiant de l’image qu’il utilise est noté, et reçoit une étiquette locale provisoire `<dépôt>:atlas-rollback-<AAAAMMJJ-HHMMSS>` (`docker image tag`) ; rien n’est poussé | 2 min |
| 5. Construire | `docker compose -p <projet> --project-directory <dossier> [--env-file …] -f <fichiers…> build`, puis tri des images (voir « Images reconstruites et images inchangées ») | 40 min |
| 6. Relancer | la même commande avec `up -d` | 10 min |
| 7. Vérifier | après 5 secondes, lecture des conteneurs toutes les 4 secondes, pendant 3 minutes au plus (voir ci-dessous) | 3 min |
| 8. Nettoyer | seulement après un déploiement réussi : retrait des plus anciennes étiquettes de retour arrière (voir plus bas) ; cette étape n’échoue jamais | 2 min |

Ce qui n’est **jamais** fait : `git reset`, `checkout` ou changement de
branche, `pull` avec fusion, `push`, option `--force`, `compose down`,
suppression de conteneur ou de volume. La seule suppression est celle
d’étiquettes `atlas-rollback-…` qu’Atlas a posées lui-même
(`docker image rm --no-prune <étiquette>`, qui retire le nom et laisse
l’image tant qu’elle a un autre nom ou un conteneur).

### Images reconstruites et images inchangées

Une application mêle souvent des images qu’elle construit (`build:`) et des
images tirées d’un registre (`image: postgres:16`), dont le nom peut servir à
d’autres applications du serveur. Seules les premières sont concernées par le
retour arrière.

Juste après le build, pour chaque service, Atlas demande à Docker quelle image
le nom du service désigne maintenant (`docker image inspect`) et la compare à
celle que son conteneur utilisait avant :

- **différente** : le service est marqué **reconstruit**. L’ancienne image
  garde son étiquette `atlas-rollback-…` ; c’est elle qu’un retour arrière
  remettra ;
- **identique** (image d’un registre, build sans changement), ou nom par
  empreinte (`nom@sha256:…`), ou nom introuvable : le service est marqué
  **inchangé**, l’étiquette provisoire posée à l’étape 4 est retirée, et un
  retour arrière ne touchera jamais à cette image ni à son nom.

Si le build échoue ou est annulé, toutes les étiquettes provisoires sont
retirées : rien n’a changé, il n’y a rien à quoi revenir.

L’historique montre, pour chaque opération, quels services ont été
reconstruits et lesquels sont inchangés.

**Nettoyage.** Après un déploiement réussi, Atlas ne garde, pour chaque image
reconstruite, que les **3** étiquettes de retour arrière les plus récentes de
l’application et retire les plus anciennes. Il ne retire que des étiquettes
de la forme exacte `…:atlas-rollback-AAAAMMJJ-HHMMSS` inscrites dans les
enregistrements de cette application : une étiquette posée à la main, même de
nom voisin, n’est jamais touchée.

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
tentée) et s’il n’a pas déjà été annulé par un retour arrière réussi. Le
bouton est **grisé, avec l’explication « rien à restaurer »**, quand ce
déploiement n’a reconstruit aucune image (le serveur refuse aussi la
demande). Après confirmation :

1. les images des services **reconstruits**, telles qu’elles tournent à ce
   moment, sont mémorisées et étiquetées à leur tour (on peut donc les
   retrouver) ; les images inchangées ne reçoivent aucune étiquette ;
2. pour chaque image reconstruite par le déploiement, Atlas vérifie que
   l’étiquette `atlas-rollback-…` désigne toujours l’image mémorisée, puis la
   remet sous le nom que le service attend
   (`docker image tag <dépôt>:atlas-rollback-… <nom d’image du service>`) ;
   si une seule vérification échoue, aucun nom n’est déplacé et les
   étiquettes de l’étape 1 sont retirées. **Le nom d’une image inchangée —
   `postgres:16`, par exemple — n’est jamais déplacé**, même s’il a été mis à
   jour entre-temps par un `pull` ;
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
- **Seules les images reconstruites reculent.** Une image tirée d’un
  registre (`image: postgres:16`), même si le déploiement a changé sa version
  dans le fichier Compose, n’est pas remise : `up -d` la relance telle que le
  fichier Compose actuel la demande.
- **Images désignées par empreinte** (`image: nom@sha256:…`) ou sans nom :
  jamais remises.
- **Image supprimée** (par un `docker image prune -a`, par exemple) : le
  retour arrière échoue à l’étape 2, sans rien toucher.
- **Service construit sous un nom partagé** : si un service a à la fois
  `build:` et un `image:` qui sert aussi à une autre application, ce nom est
  reconstruit puis remis comme les autres. Donne aux images construites un
  nom propre à l’application.
- **Déploiements d’avant cette règle** : un enregistrement qui ne dit pas
  quelles images ont été reconstruites n’offre pas de retour arrière.
- Un déploiement fait à la main (hors Atlas) entre-temps n’est pas connu :
  le retour arrière remet les images d’avant le dernier déploiement **fait
  par Atlas**.

## Journal

Chaque déploiement et chaque retour arrière laisse un enregistrement dans le
dossier de données du hub :
`~/.local/share/tmuxatlas/deploy/<application>/<identifiant>.json` (fichiers
en `0600`, dossiers en `0700`). Il contient la date, le compte, l’application,
les commits avant et après, les images d’avant avec leur état (reconstruite,
inchangée, remplacée) et leur étiquette de retour arrière s’il y en a une, chaque
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
| 4. Mémoriser les images | **le code a avancé**, les services tournent encore sur les anciennes images ; les étiquettes déjà posées sont retirées | corriger la cause (Docker injoignable ?) puis relancer « Déployer » |
| 5. Construire | **le code a avancé**, rien n’a été relancé : l’application tourne toujours sur les anciennes images ; les étiquettes provisoires sont retirées | lire la sortie du build, corriger par un nouveau commit et redéployer. Pour remettre le code à l’ancien commit en attendant : `git reset --hard <commit d’avant>` à la main (Atlas ne le fait jamais) |
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
  service reconstruit, l’étiquette de retour arrière ;
  `docker image tag <étiquette> <nom d’image du service>` puis
  `docker compose up -d --no-build`.

## Autres limites

- **Étiquettes `atlas-rollback-…` : accumulation bornée, pas nulle.** Chaque
  image reconstruite garde jusqu’à 3 anciennes versions, qui occupent le
  disque : ni `docker image prune` ni le bouton de nettoyage des images ne
  les enlèvent tant qu’elles sont étiquetées. Le nettoyage n’a lieu qu’après
  un déploiement réussi, et oublie les étiquettes dont l’enregistrement a
  disparu (au-delà de 40 opérations) ou dont le retrait a échoué. Pour voir
  et retirer ce qui reste :
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
