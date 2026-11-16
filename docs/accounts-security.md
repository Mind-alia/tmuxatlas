# Limites de sécurité des comptes

Un terminal donne les droits du compte Unix de son hôte. Les filtres de
l’interface et de l’API ne constituent pas une isolation Unix : une personne
disposant d’un terminal peut lire les fichiers accessibles à ce compte et
modifier les options tmux, notamment `@atlas-owner`.

## Un compte Linux par personne invitée

L’isolation réelle vient du système :

- chaque invité a son propre compte Linux (`useradd`, `chmod 700` sur son
  dossier, ni groupe `docker` ni `sudo`), avec son propre `tmuxatlas agent` ;
- l’administrateur lui attribue seulement l’hôte de ce peer (son empreinte de
  clé, jamais son nom) : le plus simple est un code d’appairage créé pour son
  compte depuis `/admin`, qui attribue l’hôte automatiquement ;
- un hôte n’est attribué qu’à un seul invité, et l’hôte du hub jamais.

La création du compte Linux demande root : elle reste une étape humaine.
L’interface affiche les commandes à copier, mais n’exécute rien.

## Connexion : limites et proxy

Les échecs de connexion sont comptés par adresse, par couple adresse et nom,
et **par nom d’utilisateur, toutes adresses confondues** : après 10 échecs sur
un même nom, chaque essai attend de plus en plus longtemps, jusqu’à 5 minutes
à partir de 20. Un attaquant qui change d’adresse ne devine donc pas plus vite.
Le revers : il peut ralentir la connexion du compte visé ; le compteur s’efface
après 30 minutes sans échec.

`X-Forwarded-For` n’est cru que si la requête arrive par le loopback et porte le secret partagé avec
Caddy. N’importe quel compte local peut joindre `127.0.0.1:7654` et forger cet
en-tête : sans secret configuré, il est toujours ignoré et l’adresse réelle de
la connexion (donc `127.0.0.1` derrière Caddy) sert aux limites.

Dans le `.env` du hub (`~/.config/tmuxatlas/.env`, `chmod 600`, jamais dans git) :

```bash
TMUXATLAS_PROXY_SECRET=<au moins 32 caractères, par exemple : openssl rand -hex 32>
# Facultatif, défaut X-Tmuxatlas-Proxy-Secret
TMUXATLAS_PROXY_SECRET_HEADER=X-Tmuxatlas-Proxy-Secret
```

Dans le Caddyfile, la même valeur, lue depuis l’environnement de Caddy (son
fichier d’environnement, pas le Caddyfile s’il est suivi par git) :

```caddyfile
atlas.example.com {
	reverse_proxy 127.0.0.1:7654 {
		header_up X-Tmuxatlas-Proxy-Secret {env.TMUXATLAS_PROXY_SECRET}
	}
}
```

`header_up` remplace toute valeur envoyée par le navigateur. Après le
redémarrage, vérifier dans les journaux du hub que le message
« TMUXATLAS_PROXY_SECRET unset » n’apparaît plus.

## Ce qu’un administrateur peut faire

Un administrateur TmuxAtlas voit tous les hôtes et ouvre des terminaux sur
chacun, y compris celui du hub, qui tourne sous le compte de production. Être
administrateur dans TmuxAtlas donne donc un shell sous ces comptes, même si son
propre compte Linux n’a ni `sudo` ni `docker`. Ne donnez ce rôle qu’à qui peut
déjà les utiliser.

## Ce que le hub ne transmet plus

Le hub n’envoie plus aux peers l’état des autres hôtes : un peer d’invité ne
reçoit ni les sessions de la production ni celles des autres invités. Il reste
un risque de fond : le hub peut piloter tous les peers. Un compromis du compte
du hub compromet aussi les comptes de tous les peers.
