# Boucles V2 — efficacité, progression et résultats vérifiables

Document de transmission pour GPT-6.1 Sol — 16 novembre 2026.

## Mission confiée à l’agent d’implémentation

Faire évoluer l’onglet Boucles et son moteur pour produire un travail utile, vérifiable et récupérable avec peu de supervision. Implémenter progressivement les exigences ci-dessous, en commençant par les défauts de fiabilité, puis par la décision entre les passes.

Ce document complète `boucles-evolution-produit.md`. Il porte sur la version déjà enrichie et déployée, décrite dans `boucles.md` et `boucles-deploiement.md`. Ne pas recommencer les fonctions déjà présentes. En cas de divergence, ce document précise la direction produit la plus récente ; vérifier les faits dans le code actuel avant de modifier quoi que ce soit.

L’analyse qui motive ce document est une lecture du code, sans nouvelle campagne de tests réels avec des agents. Les tests précédents ont validé des mécanismes, pas la qualité de bout en bout du travail produit.

### Résultat attendu

L’utilisateur confie un objectif. Atlas le transforme en étapes, fait exécuter le travail, examine les résultats observés et décide quoi faire ensuite. L’utilisateur comprend rapidement ce qui a avancé, quelles preuves sont disponibles et quelle décision éventuelle lui revient.

La question centrale du moteur devient :

> Après cette passe, faut-il continuer, corriger, vérifier, changer d’approche, demander une décision ou s’arrêter ?

Le nombre de passes n’est pas une mesure de réussite.

## 1. Base existante à préserver

La version actuelle possède notamment :

- création guidée et modes Mission / Entretien continu ;
- objectif, périmètre et critères textuels ;
- limite de temps et de passes ;
- worktree Git facultatif ;
- plan éditable avec étapes actuellement représentées par un booléen `done` ;
- mémoire récente persistante ;
- bilans agents structurés via `ATLAS_RESULT` ;
- commande de vérification exécutée par Atlas ;
- instantanés Git et diff par passe ;
- consignes différées et chat contextualisé ;
- partage des comptes entre boucles et propagation des délais de disponibilité ;
- détection élémentaire de stagnation ;
- revue humaine explicite ;
- bilan depuis la dernière visite, propre au compte dans le navigateur.

Ces fonctions constituent une base. Leur présence ne garantit pas encore la fiabilité des décisions ni l’efficacité du parcours.

## 2. Défauts concrets à corriger en premier

### 2.1 Les contrôles Atlas n’alimentent pas explicitement la passe suivante

**Constat :** `buildIterationPrompt` transmet l’objectif, le plan, le résumé et la mémoire des bilans agents. Les résultats de la commande de vérification exécutée par Atlas sont enregistrés dans la passe, mais ne sont pas explicitement incorporés comme diagnostic observé à la passe suivante.

**Conséquence :** la passe suivante peut ignorer le véritable échec constaté après le travail précédent et reproduire une approche inefficace.

**Exigence :** transmettre un bloc distinct de faits observés : commande, statut, code de sortie si disponible, durée, diagnostic borné, référence vers la sortie complète et lien avec l’étape concernée.

**Acceptation :** avec un faux CLI qui annonce une réussite et un contrôle qui échoue, la passe suivante reçoit explicitement l’échec Atlas et une instruction de correction. Elle ne reçoit pas une simple répétition de l’objectif général.

### 2.2 Les étapes peuvent être cochées avant qualification de l’échec

**Constat :** `runLoop` applique `CompletedSteps` avant de traiter certains cas d’interruption, de dépassement de délai, de limite fournisseur ou d’erreur. Une vérification absente est compatible avec cette mise à jour.

**Conséquence :** une étape peut apparaître terminée alors que la passe a échoué ou n’a pas été vérifiée.

**Exigence :** qualifier d’abord le résultat de la passe. Une déclaration d’achèvement doit conduire à « à vérifier » ; la validation dépend ensuite des critères et des preuves. Une interruption ne doit pas valider une étape.

**Acceptation :** un processus qui produit `completed_steps` puis échoue, est interrompu ou expire ne fait pas passer les étapes à « validée ».

### 2.3 Le mode entretien peut masquer un contrôle échoué

**Constat :** lors de `LOOP_DONE`, le moteur peut définir `blocked` pour un contrôle échoué puis remplacer ce résultat par `waiting_work` lorsque le mode est `maintenance`.

**Exigence :** rendre explicite la priorité des résultats. Un contrôle échoué doit rester visible et produire une correction ou un blocage selon la politique définie. L’attente de nouveau travail implique l’absence de problème bloquant connu.

**Acceptation :** en entretien continu, `LOOP_DONE` accompagné d’un contrôle échoué ne conduit jamais à « en attente de travail ».

### 2.4 Des preuves sont cachées lorsqu’un bilan agent est invalide

**Constat :** dans le résumé de `LoopsPage.tsx`, le contrôle Atlas se trouve dans une branche de rendu conditionnée par la présence de `iter.result`.

**Exigence :** afficher les preuves observées indépendamment de la qualité du bilan agent. Une sortie non conforme doit uniquement dégrader les informations déclaratives.

**Acceptation :** bilan JSON absent ou invalide + vérification exécutée = résultat du contrôle toujours visible.

### 2.5 Les limites sont mal distinguées

**Constat :** le résultat `limit` peut venir du temps ou du nombre de passes, alors que le libellé du résumé parle de limite de passes.

**Exigence :** conserver un motif précis : temps épuisé, nombre de passes atteint, limite utilisateur ou autre motif réellement implémenté. Montrer le travail restant et la marche à suivre.

**Acceptation :** les scénarios temps et passes ont chacun un bilan exact, sans présenter un objectif atteint.

### 2.6 La validation humaine manque de traçabilité

**Constat :** `Review` marque l’objectif `achieved` sans enregistrer les critères examinés, une réserve ou le détail de la décision.

**Exigence :** enregistrer auteur, date, résultat examiné, critères validés et commentaire éventuel. Permettre de demander une correction. Si une personne accepte malgré un contrôle échoué, enregistrer explicitement cette réserve ; ne pas réécrire le contrôle comme réussi.

**Acceptation :** une revue permet de retrouver ce qui a été accepté et par qui. Une modification ultérieure ne réutilise pas silencieusement une ancienne validation comme preuve du nouveau résultat.

## 3. Donner une responsabilité précise à chaque passe

### Cycle cible

1. Comprendre l’objectif et établir l’état initial.
2. Proposer ou compléter un plan.
3. Sélectionner une étape éligible.
4. Exécuter une action bornée sur cette étape.
5. Vérifier le résultat.
6. Décider de la suite et enregistrer la raison.
7. Préparer une revue finale lorsque les critères sont satisfaits.

Une mission simple peut regrouper diagnostic et planification. Ne pas ajouter systématiquement des appels agents sans valeur.

### Modèle d’étape

Remplacer progressivement le booléen `done` par un état plus expressif :

- à faire ;
- en cours ;
- à vérifier ;
- validée ;
- bloquée.

Chaque étape doit posséder :

- un identifiant stable ;
- un résultat attendu ;
- un périmètre ;
- des dépendances éventuelles ;
- un critère de sortie ;
- les preuves associées ;
- les tentatives et leur résultat ;
- la raison d’un blocage éventuel.

Distinguer les preuves automatisées, les déclarations d’agent et les validations humaines. Un ancien `done: true` ne doit pas être migré en preuve automatisée inventée.

### Sélection du travail

Le moteur sélectionne une étape dont les dépendances sont satisfaites. Le prompt explique précisément l’étape à traiter et le critère de sortie. Il inclut le contexte global nécessaire, sans inviter à modifier arbitrairement d’autres parties du projet.

Un agent peut proposer une modification du plan, mais celle-ci doit devenir une décision traçable. Définir quels ajustements sont permis automatiquement et lesquels doivent être présentés à l’utilisateur selon l’autonomie choisie.

### Exemple de parcours

Objectif : fiabiliser l’inscription.

- Étape 1 : reproduire le doublon d’adresse et écrire ou identifier un scénario de contrôle.
- Étape 2 : corriger la cause dans le périmètre convenu.
- Étape 3 : vérifier le parcours complet, y compris le scénario mobile concerné.
- Revue : présenter le diff, les résultats de contrôle et les limites restantes.

Une passe de diagnostic peut être utile sans modifier de fichier.

## 4. Construire une décision explicite entre les passes

| Résultat observé | Décision attendue |
| --- | --- |
| Contrôle réussi et critère satisfait | Valider l’étape puis choisir la suivante |
| Contrôle échoué | Faire corriger avec le diagnostic exact |
| Preuves insuffisantes | Exécuter une vérification ciblée |
| Échec répété malgré plusieurs tentatives | Changer d’approche ou demander une décision |
| Compte indisponible | Attendre ou changer de compte, sans confondre cela avec une stagnation du travail |
| Budget épuisé | Arrêter avec un bilan exploitable |
| Tous les critères satisfaits | Préparer la revue finale |
| Entretien sans travail pertinent | Se mettre en attente avec un motif explicite |

Implémenter cette décision dans une fonction testable, distincte de l’exécution de processus et du rendu UI. Enregistrer la décision et sa raison dans l’historique.

### État initial et régressions

Avant de modifier le projet, établir les contrôles pertinents déjà réussis ou échoués. Les rapports suivants doivent distinguer :

- problème déjà présent ;
- problème résolu ;
- nouvelle régression ;
- contrôle non exécuté ou non comparable.

Ne pas prétendre qu’une simple comparaison de texte suffit pour tous les outils. Lorsque le format ne permet pas une comparaison fiable, afficher cette limite et conserver les sorties.

### Contrôles adaptés

Permettre des contrôles ciblés par étape et une validation plus complète aux jalons. Éviter de relancer toute une suite coûteuse après chaque modification triviale lorsque des contrôles ciblés suffisent, sans supprimer les vérifications nécessaires avant livraison.

## 5. Une mémoire structurée et durable

La mémoire actuelle concatène des bilans et coupe le début au-delà de 24 Ko. Une décision utilisateur peut donc disparaître du contexte transmis.

Organiser la mémoire en quatre catégories :

| Catégorie | Politique |
| --- | --- |
| Objectif, contraintes, décisions utilisateur | Durable et explicitement versionnée |
| Plan et preuves des étapes | Durable, structurée |
| Tentatives échouées et causes | Synthèse durable avec références |
| Détails des dernières passes | Fenêtre récente bornée |

Les consignes utilisateur ne doivent pas dépendre de la rétention des logs ou des bilans agents. Conserver leur statut : en attente, appliquée, remplacée ou annulée.

Le contexte de chaque passe doit permettre de répondre :

- que faut-il accomplir maintenant ?
- quelles contraintes restent applicables ?
- qu’a-t-on déjà essayé ?
- quelles preuves et quels échecs justifient la prochaine action ?

La compression du contexte ne doit pas effacer silencieusement une contrainte. Conserver l’historique complet hors du prompt et utiliser des références consultables pour les détails volumineux.

## 6. Une interface orientée vers les décisions

### Vue principale d’une mission

Exemple de contenu cible :

> **Inscription — correction en vérification**  
> 2 étapes validées sur 4 · 38 min écoulées  
> Dernier résultat : doublon d’adresse corrigé  
> Contrôle : un scénario reste en échec  
> Prochaine action : corriger la validation mobile  
> **Voir le changement · Orienter la suite · Pause**

Afficher une progression à partir des étapes et de leurs preuves. Ne pas fabriquer de pourcentage de complétion à partir du nombre de passes.

### Vues distinctes

**Progression**

- plan ;
- étape actuelle ;
- preuves et blocages ;
- prochaine action et raison ;
- décision attendue, formulée précisément.

**Changements**

- liste de fichiers ;
- diff lisible avec navigation ;
- comparaison de la passe sélectionnée ;
- comparaison globale depuis le début de mission ;
- branche ou worktree concerné ;
- résultats de contrôle associés au changement examiné.

Les modifications déjà présentes au début doivent être distinguées de celles produites par la mission. Un diff tronqué ou indisponible doit être annoncé.

**Historique**

Une chronologie de décisions : problème observé, action, résultat, preuve et suite choisie. Les logs sont accessibles depuis chaque événement, sans constituer le contenu principal de cette vue.

**Logs**

Diagnostic technique, recherche et sélection de passe. Permettre de suivre la passe en direct ou de rester volontairement sur une ancienne passe, sans déplacement automatique inattendu.

### Parcours de sortie

Présenter un bilan global : ce qui a été réalisé, ce qui reste, les critères satisfaits, les réserves et les changements à intégrer. La validation doit porter sur un résultat identifiable.

L’intégration doit avoir un parcours concret adapté au dépôt : préparer la branche, le patch ou une proposition de changement selon les outils disponibles et l’autorisation donnée. Ne pas assimiler la validation de qualité à une autorisation de fusion ou de publication.

## 7. Une exécution rentable et équitable

### Comptes et priorité

La réservation actuelle évite l’utilisation concurrente d’un compte par deux passes, mais ne garantit pas une répartition équitable.

Ajouter :

- priorité explicite des missions ;
- ordre d’attente stable ;
- protection contre une attente indéfinie des priorités faibles ;
- motif d’attente visible ;
- libération fiable des réservations à la fin, à l’erreur, à l’annulation et au redémarrage.

Définir et tester la politique. Exemple acceptable : priorité puis ancienneté d’attente, avec vieillissement progressif. Éviter qu’une boucle continue récupère systématiquement le compte avant toutes les autres.

### Concurrence sur les fichiers

Deux comptes différents peuvent modifier le même dossier. Une réservation de compte ne protège pas le code.

Ajouter une protection au niveau du dossier de travail canonique : une seule boucle modificatrice à la fois dans le même dossier, ou isolation explicite dans des worktrees distincts. Traiter les chemins équivalents et les liens symboliques.

La protection interne ne garantit pas l’absence de modifications par un terminal externe. Détecter et signaler les changements extérieurs lorsque possible ; ne jamais les annuler silencieusement.

### Répartition des rôles

Conserver un agent principal pour la continuité. Changer de compte pour la disponibilité. Permettre une revue indépendante pour les changements qui le justifient, en transmettant le diff, les critères et les preuves.

Ne pas systématiser plusieurs agents sur toutes les tâches. Une revue supplémentaire doit répondre à une incertitude précise.

### Budgets

Afficher temps passé, passes et attente. Si des métriques fiables de tokens ou de coût sont disponibles, les intégrer avec leur provenance. Ne pas inventer de consommation ni de prix à partir de simples durées.

## 8. Performance et persistance pour les missions longues

### Constats

- La liste recharge régulièrement les boucles et leurs historiques.
- `saveLocked` réécrit l’ensemble des boucles dans un fichier JSON.
- Les diffs sont bornés, mais l’historique global continue de croître.

### Architecture attendue

Séparer :

- le résumé de chaque mission pour la liste ;
- l’état courant pour le détail ;
- les événements et passes paginés ;
- les artefacts volumineux : logs, diffs, sorties de contrôle.

Utiliser des mises à jour incrémentales ou le transport d’événements existant si cela s’intègre proprement. Charger les artefacts à la demande. Prévoir une politique explicite de rétention et d’archivage sans supprimer de preuve référencée par une revue active.

Les erreurs de persistance doivent être traitées. Une action ne doit pas être annoncée comme durable si son enregistrement a échoué. Définir la reprise après écriture partielle ou redémarrage.

### Validation de charge

Utiliser des données simulées représentant de nombreuses missions et des milliers de passes. Mesurer : taille des réponses de liste, latence de sauvegarde, mémoire et réactivité du détail. Fixer les seuils à partir d’un état initial mesuré et documenter le gain.

## 9. Mesurer l’efficacité réelle

Indicateurs prioritaires :

- temps entre lancement et résultat accepté ;
- nombre de passes sans progrès identifié ;
- nombre d’interventions utilisateur par mission ;
- temps passé en attente de compte ou de décision ;
- régressions introduites puis corrigées ;
- résultats refusés en revue et raisons.

Définir « progrès » à partir des étapes, preuves et décisions utiles. Une passe de diagnostic peut représenter un progrès même sans diff.

Les indicateurs doivent être calculés à partir d’événements persistés, avec une définition claire. Éviter les scores arbitraires et les tableaux de bord qui encouragent simplement plus d’activité.

## 10. Ordre d’implémentation

### Lot A — Fiabilité des résultats

Corriger les six défauts de la section 2. Ajouter les tests de régression correspondants. Transmettre les contrôles observés à la passe suivante et distinguer les motifs de fin.

**Livrable :** états cohérents et preuves toujours visibles.

### Lot B — Décision entre les passes

Introduire le modèle d’étape, les critères de sortie et une fonction de décision explicite. Ajouter l’état initial, la correction ciblée et les transitions de validation.

**Livrable :** chaque passe a une tâche précise et une suite justifiée.

### Lot C — Mémoire et revue

Structurer la mémoire durable, les tentatives et les consignes. Construire la chronologie, le bilan global et la revue traçable des critères et des changements.

**Livrable :** continuité du raisonnement et résultat récupérable.

### Lot D — Orchestration et montée en charge

Ajouter équité des comptes, priorités, protection des dossiers et revue indépendante optionnelle. Séparer résumés, événements et artefacts. Mesurer l’efficacité.

**Livrable :** plusieurs missions utilisables sur la durée sans supervision permanente ni dégradation progressive.

## 11. Tests de bout en bout à prévoir

Utiliser des CLI simulés et des dépôts temporaires pour les tests automatisés.

1. L’agent annonce une réussite, le contrôle échoue : l’étape reste à vérifier et la passe suivante reçoit le diagnostic.
2. L’agent annonce des étapes terminées puis sort en erreur : aucune validation indue.
3. L’agent est interrompu ou atteint le délai : aucune étape validée à tort.
4. Entretien continu + `LOOP_DONE` + contrôle échoué : résultat bloqué ou correction, jamais attente de travail sain.
5. Bilan invalide : les contrôles observés restent consultables.
6. Échec préexistant : il est distingué d’une nouvelle régression lorsque les données le permettent.
7. Dépendance non satisfaite : l’étape dépendante n’est pas exécutée.
8. Changement de CLI : objectif, contraintes, plan et tentatives échouées sont transmis.
9. Compaction de mémoire : les consignes utilisateur durables restent disponibles.
10. Deux missions concurrentes : ordre de priorité et équité respectés, sans double réservation.
11. Deux chemins désignent le même dossier : protection effective contre les écritures concurrentes internes.
12. Annulation et redémarrage : réservations libérées, état interrompu exact, reprise explicite.
13. Revue avec réserve : auteur, date, critères et résultat examiné sont enregistrés.
14. Nouveau changement après revue : ancienne validation conservée historiquement mais non appliquée au nouveau résultat.
15. Mission longue simulée : la liste reste légère, les historiques sont paginés et les artefacts chargés à la demande.
16. Échec d’écriture : l’API ne prétend pas que l’action a été sauvegardée.

Compléter avec un test navigateur des parcours essentiels : créer, suivre, examiner un échec, orienter, reprendre et revoir le résultat. Ne pas se limiter au build TypeScript ou à la présence de boutons.

## 12. Fichiers et points d’entrée

- `pkg/loops/runner.go` : `runLoop`, `executeIteration`, `buildIterationPrompt`, transitions et conditions de fin.
- `pkg/loops/mission.go` : modèles, mémoire, parsing, `Review`, `SetPlan`, ordonnanceur partagé et stagnation.
- `pkg/loops/loops.go` : données persistées, migration, `saveLocked`, lecture des passes.
- `pkg/loops/http.go` : contrats API.
- `pkg/server/server.go` et `pkg/server/account_guard.go` : routes et accès.
- `web/src/components/loops/LoopsPage.tsx` : écran, résumé, navigation et suivi.
- `web/src/components/loops/LoopPlan.tsx` : édition du plan.
- `web/src/components/loops/SubscriptionPool.tsx` : comptes et disponibilité.
- `web/src/components/loops/loopsApi.ts` : contrats frontend.
- `pkg/loops/mission_test.go`, `pkg/loops/loops_test.go` et `web/src/components/loops/LoopsPage.test.tsx` : tests existants.

## 13. Consignes de réalisation pour Sol 6.1

- Lire les instructions applicables du dépôt et examiner les changements présents. L’espace de travail contient déjà des modifications : les préserver.
- Faire évoluer la version actuelle ; ne pas reconstruire l’onglet depuis zéro.
- Prévoir des migrations explicites. Ne pas inventer de preuves pour les données anciennes, changer silencieusement l’autonomie ni relancer des missions interrompues.
- Séparer la décision métier des processus CLI, du stockage et de l’UI. Extraire les composants lorsque les responsabilités le justifient.
- Garder les critères subjectifs distincts des contrôles automatisés. Ne pas assimiler code de sortie zéro, déclaration d’agent et réussite métier.
- Préserver les droits d’accès et ne pas exposer les détails d’autres propriétaires dans les informations de disponibilité.
- Tester chaque lot avec des scénarios significatifs. Les tests Go impliquant tmux doivent utiliser un répertoire isolé réellement créé et un environnement sans `TMUX` hérité ; éviter les collisions entre suites concurrentes.
- Ne pas lancer de missions réelles consommatrices de comptes uniquement pour les tests automatisés.
- Si un déploiement est demandé, reconstruire le frontend puis le binaire embarqué, utiliser le mécanisme de redémarrage existant, préserver les sessions tmux, conserver un retour arrière et vérifier le service effectivement installé.
- Documenter ce qui a été réalisé, les résultats de validation et les limites restantes. Ne pas annoncer « tout terminé » si un lot ou un critère obligatoire reste absent.

## Critère final de réussite

Une mission doit produire un résultat vérifiable avec peu de supervision, expliquer ses décisions et s’arrêter lorsque continuer n’apporte plus de valeur. L’utilisateur doit pouvoir comprendre et accepter le résultat sans reconstituer le raisonnement à partir des logs.
