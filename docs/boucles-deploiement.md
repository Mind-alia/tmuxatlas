# Déploiement Boucles — 16 novembre 2026

## Version installée

Service utilisateur `tmuxatlas.service`, binaire `/root/.local/bin/tmuxatlas`, interface `http://127.0.0.1:7654/loops`.

Le binaire embarque le frontend compilé. SHA-256 : `99a609448e0f91c4ad271179642d46d44cd4d66d6af77c7ea4161c58a70fb536`.

## Évolutions livrées

- Création guidée en quatre étapes, modes Mission et Entretien continu, critères, périmètre et limites.
- Plan éditable pendant une pause, transmission des étapes et mémoire récente aux agents.
- Comptes réservés pendant les passes et délais de disponibilité partagés entre boucles.
- Bilans structurés, contrôles configurables avec sortie bornée et durée enregistrée.
- Détection d’échecs répétés, de décisions répétées et de cycles de modifications.
- Diff propre à chaque passe, revue et validation explicite du résultat.
- Consignes différées, chat contextualisé, activité depuis la dernière visite propre au compte dans ce navigateur.
- Corrections de sélection sous filtre, de libellés et du bouton Entretien continu.

## Vérifications

- Suite Go complète : `go test -p 1 ./...`, sans TMUX hérité, avec répertoire tmux de test isolé.
- Tests supplémentaires du moteur après les derniers ajustements : réussis.
- Trois tests de l’onglet Boucles : réussis, avec délai de test adapté à la charge de compilation.
- Build TypeScript et Vite : réussi.
- Contrôle de santé du service installé : réussi.
- `/loops` : HTTP 200 ; empreinte du fichier JavaScript servi identique au fichier compilé.
- Accès anonyme aux endpoints plan, instruction et revue : HTTP 401.
- Redémarrage via `scripts/restart-server.sh` : les 10 sessions tmux et leurs processus sont conservés.

Aucune vraie mission d’agent n’a été lancée pour la validation. Les intégrations agents sont couvertes par les tests simulés ; la qualité des résultats dépend des instructions et des critères configurés.

## Retour arrière

Binaire précédent : `/root/.local/share/tmuxatlas/boucles-deploy.cpZGxw/tmuxatlas`.

Restaurer ce binaire par remplacement atomique puis utiliser `scripts/restart-server.sh`. Les données existantes ne sont pas supprimées par ce déploiement.

Les limites fonctionnelles sont décrites dans [boucles.md](boucles.md).
