# Secrets

La page **Secrets** (`/secrets`) est un coffre pour les variables d'environnement des applications : au lieu de traîner en clair dans des fichiers `.env`, les valeurs sont stockées chiffrées, et ne sont écrites en clair qu'au moment où on le demande. Elle est réservée aux administrateurs.

## Fonctionnement

- Une **application** est un nom (`[a-z0-9][a-z0-9_-]{0,62}`) et une liste de variables `NOM=valeur`. Les noms de variables suivent la règle des shells (lettres, chiffres, `_`, pas de chiffre en premier).
- Chaque **valeur** est chiffrée séparément avec [age](https://age-encryption.org) pour la clé publique du coffre. Les **noms** restent lisibles.
- Comme le chiffrement n'utilise que la clé publique, **ajouter, remplacer ou importer des valeurs ne demande pas de déverrouiller**.
- **Révéler** une valeur, **exporter** une application en `.env` ou l'**appliquer** demande la clé privée, donc le coffre déverrouillé.

### Clé maîtresse

À la création du coffre, l'administrateur choisit une phrase de passe (12 caractères au moins). Le hub génère une identité age X25519 :

- la **clé publique** est enregistrée en clair ;
- la **clé privée** est enregistrée chiffrée par la phrase de passe (age en mode phrase de passe, dérivation scrypt) ;
- la phrase de passe n'est enregistrée nulle part.

Au repos, le hub ne peut donc pas lire les valeurs. **Déverrouiller** = fournir la phrase de passe : la clé privée déchiffrée reste en mémoire 10 minutes, sans prolongation quand on s'en sert, puis est oubliée. Un redémarrage du hub verrouille aussi.

**Le déverrouillage appartient à la session de navigateur qui a donné la phrase de passe.** Une autre session, même du même administrateur, voit « déverrouillé dans une autre session » et ne peut ni révéler, ni exporter, ni appliquer tant qu'elle n'a pas donné la phrase de passe elle-même ; chaque session a alors ses propres 10 minutes. Se déconnecter, ou voir sa session révoquée (compte désactivé ou supprimé, passkey supprimée, expiration), met fin au déverrouillage de cette session. La clé privée quitte la mémoire avec le dernier déverrouillage. **Verrouiller** l'oublie tout de suite, pour toutes les sessions. Le hub ne garde de la session qu'une empreinte (SHA-256) de son cookie. Sans authentification (`TMUXATLAS_SECRETS=on`), il n'y a pas de session : le déverrouillage est alors commun à tous ceux qui atteignent le hub.

Après 5 phrases de passe refusées de suite, chaque nouvel échec bloque les essais : 30 secondes, puis le double à chaque fois, jusqu'à 15 minutes ; les essais faits pendant un blocage sont refusés sans être examinés et notés au journal. Une heure sans échec remet le compte à zéro, un déverrouillage réussi aussi. Ce compteur est en mémoire : il repart de zéro au redémarrage du hub.

**La clé publique est épinglée au démarrage du hub** (ou à la création du coffre) : toutes les valeurs sont chiffrées pour cette clé-là. Si `vault.json` montre ensuite une autre clé publique, ou disparaît, le coffre se déclare incohérent — la page l'affiche — et refuse de chiffrer comme de déverrouiller jusqu'à ce que le fichier d'origine soit remis en place (ou que le hub soit redémarré, si le changement est voulu). Au déverrouillage, la clé privée déchiffrée doit correspondre à la clé publique épinglée.

### Où vivent les fichiers

Dans le dossier de données du hub, `~/.local/share/tmuxatlas/secrets/` sous Linux (dossier en 0700, fichiers en 0600) :

| Fichier | Contenu |
|---|---|
| `vault.json` | clé publique en clair, clé privée chiffrée par la phrase de passe |
| `apps/<nom>.json` | une application : noms en clair, valeurs chiffrées, cible |
| `audit.log` | le journal d'audit |
| `.lock` | verrou des écritures |

Les écritures passent par un fichier temporaire puis un renommage, sous verrou : deux modifications simultanées ne se perdent pas. Les fichiers temporaires laissés par un arrêt brutal sont retirés au démarrage. Un fichier d'application abîmé ou modifié à la main (JSON invalide, nom de variable invalide ou en double…) est signalé « illisible » dans la liste, sans gêner les autres applications : il ne peut plus être lu ni appliqué, seulement supprimé.

**Sauvegarde** : ce dossier se copie tel quel. Une copie sans la phrase de passe ne sert à rien à celui qui la vole — ni à vous.

## Importer un `.env`

Collez le texte dans « Importer un fichier .env ». Il est analysé sur le serveur et chaque valeur est chiffrée aussitôt ; une ligne invalide annule tout l'import. Sont compris : lignes vides, commentaires `#`, préfixe `export `, valeurs nues (un ` #` en fin de ligne est un commentaire), entre apostrophes (texte littéral), entre guillemets (échappements `\n`, `\r`, `\t`, `\\`, `\"`, `\$`). Une valeur entre apostrophes ou guillemets peut tenir sur plusieurs lignes. Un nom répété garde sa dernière valeur. Les messages d'erreur donnent le numéro de la ligne, jamais son contenu.

**Les références entre variables ne sont pas conservées.** Une valeur comme `URL=http://${HOST}:8080` est stockée telle quelle, puis réécrite entre apostrophes à l'application (`URL='http://${HOST}:8080'`) : Compose ne remplace alors plus `${HOST}`. Remplacez ces références par leur valeur avant d'importer.

## Appliquer

Une application peut avoir une **cible** : le chemin d'un fichier `.env` et, si on veut, le nom d'un conteneur à redémarrer. « Appliquer » (coffre déverrouillé, confirmation à l'écran) :

1. refuse une application sans variable (elle remplacerait un fichier qui marche par un fichier vide) ;
2. écrit le fichier, **en clair**, en 0600, par fichier temporaire puis renommage ; si le fichier contient déjà exactement ce texte, rien n'est réécrit (sauf pour le remettre en 0600) ;
3. garde le fichier remplacé dans `<fichier>.bak` (0600). Une sauvegarde identique n'est pas réécrite ; une sauvegarde différente passe d'abord en `<fichier>.bak.1` : on garde donc les deux derniers contenus remplacés. Un fichier existant de plus de 4 Mio est refusé ;
4. redémarre le conteneur, s'il y en a un, par le même chemin que le bouton « Redémarrer » de la page Conteneurs — donc avec ses réglages (`TMUXATLAS_DOCKER`, refus tant qu'un compte non administrateur est actif, voir [ops.md](ops.md)).

Si le redémarrage échoue, le fichier est quand même écrit, et la page le dit.

**Le fichier écrit est un nouveau fichier, lisible par le seul compte du hub.** Deux pièges, rappelés dans la confirmation :

- un conteneur qui monte **le fichier lui-même** (`- ./.env:/app/.env`) garde l'ancien fichier, même après un redémarrage : il faut le recréer, ou monter le dossier ;
- un processus qui lit le fichier **sous un autre utilisateur** (UID différent dans le conteneur, autre compte de la machine) ne peut plus le lire, quels que soient les droits de l'ancien fichier.

Les sauvegardes `.env.bak` et `.env.bak.1` contiennent des secrets en clair : ajoutez `*.bak` et `*.bak.1` (ou `.env*`) aux `.gitignore` et `.dockerignore` des projets visés, qui n'ignorent souvent que `.env`.

Le chemin cible est vérifié à l'enregistrement **et de nouveau à chaque application** :

- absolu et normalisé (pas de `..`, de `.` ni de barre finale) ;
- son dossier existe et, liens symboliques suivis, se trouve dans le dossier personnel du compte du hub ;
- pas dans le dossier du coffre ni dans les dossiers de configuration et de données d'Atlas ;
- le fichier n'est pas un lien symbolique (même vers un fichier du dossier personnel) ni un dossier ;
- l'écriture se fait ensuite relativement au dossier ouvert et vérifié, sans pouvoir sortir du dossier personnel (`os.Root`) : un composant du chemin remplacé par un lien après la vérification ne déplace pas l'écriture. Il reste un instant, entre la résolution du chemin et l'ouverture du dossier, où un processus du même compte Unix pourrait substituer un autre dossier *du dossier personnel* ; ce processus peut de toute façon y écrire directement ;
- il s'appelle `.env`, `.env.<suffixe>` ou `<nom>.env` : « Appliquer » remplace tout le fichier, on l'empêche donc de viser autre chose qu'un fichier d'environnement.

Dans le fichier écrit, une valeur faite de caractères sans risque est écrite nue, sinon entre apostrophes, sinon entre guillemets avec échappements (`\\`, `\"`, `\$`, `` \` ``, `\n`, `\r`). Docker Compose et les bibliothèques dotenv relisent ce format ; `docker run --env-file`, lui, ne comprend pas les guillemets.

**Un redémarrage ne suffit pas toujours.** Docker fixe les variables d'un conteneur à sa création. Un conteneur créé par Compose avec `env_file:` ne voit le nouveau fichier qu'après `docker compose up -d` (recréation), pas après un redémarrage. Le redémarrage suffit quand l'application lit elle-même le fichier, monté dans le conteneur.

## Journal d'audit

`audit.log` reçoit une ligne JSON par action, en ajout seul : date, compte (ou `passkey` sans comptes), action, application, nom de variable, et selon le cas la cible ou un nombre de variables. **Jamais de valeur ni de phrase de passe.** Les déverrouillages refusés ou bloqués y figurent, ainsi que la fin d'un déverrouillage par déconnexion. Une révélation, un export ou une application n'a lieu que si sa ligne a pu être écrite ; à l'inverse, une modification (valeur, import, cible) n'est notée qu'une fois réellement écrite. La page affiche les 50 dernières lignes. Le fichier n'est pas tourné automatiquement.

## Activation

| `TMUXATLAS_SECRETS` | Effet |
|---|---|
| absent | coffre actif si le hub a une authentification ; refusé sans authentification |
| `on` | actif même sans authentification : seule l'origine des requêtes est alors vérifiée, il n'y a ni compte ni session, et tout processus ou toute personne qui atteint le port du hub peut utiliser le coffre (et profiter d'un déverrouillage en cours). À réserver à un hub en écoute locale sur une machine à un seul utilisateur |
| `off` | désactivé |

## API

Toutes les routes sont sous `/api/secrets/`, réservées aux administrateurs en mode comptes, et répondent avec `Cache-Control: no-store`. Tout ce qui modifie quelque chose **ou renvoie une valeur** est un `POST`, `PUT` ou `DELETE` soumis aux contrôles d'origine et de jeton CSRF des autres modifications.

| Route | Rôle |
|---|---|
| `GET status` | initialisé ? déverrouillé ? jusqu'à quand ? |
| `POST init`, `POST unlock` (`{"passphrase"}`), `POST lock` | clé maîtresse |
| `GET apps` | applications et noms de variables |
| `POST apps` (`{"name"}`), `DELETE apps/{app}` | créer, supprimer |
| `PUT apps/{app}/vars/{name}` (`{"value"}`), `DELETE apps/{app}/vars/{name}` | définir, supprimer |
| `POST apps/{app}/vars/{name}/reveal` | une valeur en clair (déverrouillé) |
| `POST apps/{app}/import` (`{"text"}`) | import dotenv |
| `POST apps/{app}/export` | l'application en dotenv (déverrouillé) |
| `PUT apps/{app}/target` (`{"env_file","container"}`) | cible ; deux chaînes vides la retirent |
| `POST apps/{app}/apply` | écrire la cible, redémarrer (déverrouillé) |
| `GET audit?limit=N` | dernières lignes du journal |

`status` porte `unlocked` (pour la session qui demande), `unlocked_elsewhere` et, pour un coffre incohérent, `problem`. Un coffre verrouillé, ou déverrouillé seulement dans une autre session, répond `423`, une mauvaise phrase de passe `403` (code `wrong_passphrase`), trop d'essais `429` avec `Retry-After`.

## Limites, dites honnêtement

- **Le coffre protège les secrets au repos, pas une application qui tourne.** Une fois appliqué, le fichier cible est un `.env` en clair comme avant, et les variables sont visibles dans le conteneur (`docker inspect`, `/proc`). Le gain : une seule copie maîtresse chiffrée, des sauvegardes du coffre qui ne fuient rien, plus de secrets collés dans des notes ou des dépôts, et une trace de qui a lu quoi.
- **Phrase de passe perdue = secrets perdus.** Il n'y a ni récupération ni porte dérobée. Il n'y a pas non plus, pour l'instant, de changement de phrase de passe dans l'interface.
- **Tant que le coffre est déverrouillé**, la clé privée est dans la mémoire du hub : quiconque contrôle le compte Unix du hub à ce moment-là peut la lire. Les valeurs révélées passent par le navigateur de l'administrateur.
- **Tous les comptes Atlas partagent le compte Unix du hub** (voir [accounts-security.md](accounts-security.md)). Un compte non administrateur ne peut pas utiliser l'API du coffre, mais depuis son terminal il agit sous le même compte Unix que le hub. Il peut donc : lire les fichiers `.env` appliqués et leurs `.bak` (en clair) ; lire la mémoire du hub pendant un déverrouillage ; copier `vault.json` et **attaquer la phrase de passe hors ligne**, sans la limite d'essais (scrypt ralentit chaque essai, il ne remplace pas une phrase de passe longue) ; **intercepter les valeurs saisies ensuite**, par exemple en remplaçant `vault.json` avant un redémarrage du hub — l'épinglage de la clé publique ne protège qu'un hub déjà démarré — ou en modifiant le binaire ou la configuration du hub. Le coffre ne crée aucune séparation entre comptes : sur un hub qui a des comptes non administrateurs auxquels on ne confierait pas ces secrets, il ne faut pas l'utiliser.
- **En mode passkey**, le socket Unix local du hub peut permettre à un processus du compte du hub de renouveler le jeton d’amorçage, donc d’enrôler une passkey et d'obtenir une session d'administrateur : il lit alors les noms, les cibles et le journal. Les valeurs restent hors d'atteinte sans la phrase de passe, le déverrouillage étant lié à la session qui l'a donnée.
- **Le chiffrement ne signe rien.** Quelqu'un qui peut écrire dans le dossier du coffre peut y remplacer une valeur chiffrée par une autre (la clé publique suffit pour chiffrer) ou supprimer des lignes du journal. Le journal est une trace, pas une preuve.
- **Le hub voit la phrase de passe et les valeurs** au moment où elles lui sont envoyées (déverrouillage, ajout, import) : il faut un hub de confiance et une connexion HTTPS.
- **Ce n'est pas le format SOPS.** Ce sont des fichiers JSON dont chaque valeur est un fichier age « armored » ordinaire.

## Lire le coffre sans Atlas

Les valeurs se déchiffrent avec l'outil `age` standard et `jq`, sans le hub :

```sh
cd ~/.local/share/tmuxatlas/secrets

# 1. La clé privée : age demande la phrase de passe du coffre.
#    /dev/shm est en mémoire : la clé en clair ne touche pas le disque.
jq -r .identity vault.json > /dev/shm/identity.age
age -d -o /dev/shm/key.txt /dev/shm/identity.age

# 2. Une valeur.
jq -r '.vars[] | select(.name == "API_TOKEN") | .value' apps/mon-app.json > /dev/shm/value.age
age -d -i /dev/shm/key.txt /dev/shm/value.age

# 3. Effacer la clé en clair.
rm -f /dev/shm/key.txt /dev/shm/identity.age /dev/shm/value.age
```

`jq -r .public_key vault.json` donne la clé publique (`age1…`) : `age -a -r <clé publique>` produit une valeur que le coffre saura lire.
