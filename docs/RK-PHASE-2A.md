# Phase 2A — protection des données manuelles

Cette branche dépend de la PR #1 (`feat/rk-phase1-foundations`, commit
`90778ca182b664e873d631f5dc373e662d3465c5`). Elle en hérite par ascendance,
sans recopier ses fichiers. La PR 2A cible `develop` en brouillon : tant que #1
n'est pas fusionnée, GitHub affiche aussi son commit. Le diff propre à 2A est
`git diff 90778ca..HEAD`. Revoir/fusionner #1 avant 2A ; si #1 est squashée,
rebaser uniquement les commits 2A sur `develop` avant une éventuelle fusion.
Aucun déploiement, nouveau connecteur ou changement de `main` n'est requis.

## Stockage et règles

La migration additive `20261029000001_progress_provenance` conserve les colonnes
historiques comme projection compatible. Elle ajoute :

- `progress_manual_overrides` : heures et deux compteurs de succès, par plateforme utilisateur ;
- `progress_observations` : mêmes métriques externes et date d'observation ;
- `user_games.play_status_manual` et `user_game_platforms.ownership_manual` :
  protection des statuts explicitement choisis.

Toutes les métriques existantes sont copiées comme manuelles, même zéro et même
avec `sync_from_source=true`. Les NULL restent NULL. Les statuts existants sont
protégés par prudence. Les observations sont copiées depuis les plateformes
externes liées, seulement si le jeu externe appartient au même utilisateur.
Aucune valeur historique n'est recalculée pendant la migration.

Une édition écrit dans les overrides ; une synchronisation écrit dans les
observations. La projection choisit `COALESCE(manuel, observation)` par champ :
zéro n'est jamais une absence. Une réponse partielle ou sans compteur ne supprime
pas une valeur connue. Un snapshot daté antérieur au dernier accepté est ignoré.
Les heures externes gardent la règle historique du maximum cumulatif ; les
totaux ne sont jamais additionnés. Les observations restent stockées même si
elles sont masquées. Les statuts manuels, y compris `not_started` et
`no_longer_owned`, ne sont pas promus par une synchronisation.

Les écritures de domaine prennent le verrou du `user_game` avant la plateforme,
et conservent ce verrou jusqu'à la fin de la transaction : observation, override
et projection sont atomiques. Deux éditions du même champ sont sérialisées ; la
dernière écriture validée gagne. Il n'y a pas de contrat ETag/CAS avec le client.
Une édition et une sync concurrentes donnent toujours priorité au manuel.
Les plateformes sans boutique utilisent une comparaison SQL compatible avec NULL.

Les nouvelles tables n'acceptent aucun identifiant utilisateur/compte indépendant :
leur propriétaire est exclusivement celui de la plateforme référencée, avec
suppression en cascade. Le domaine vérifie l'appartenance du jeu, de la plateforme
et de tout lien externe ; le worker charge aussi le jeu externe sous l'utilisateur
du job. L'import ne reprend jamais les identifiants des plateformes exportées.

## API, export/import et sauvegardes

Les URL, méthodes et champs des réponses actuelles restent disponibles. Les
requêtes plateforme acceptent désormais `achievements_unlocked` et
`achievements_total`, avec zéro valide. Dans la mise à jour partielle existante,
omission ou JSON `null` conserve la valeur manuelle. Le retour explicite à la
source utilise, champ par champ :

```json
{ "clear_hours_override": true }
```

Les autres drapeaux sont `clear_achievements_unlocked_override` et
`clear_achievements_total_override`. Le drapeau d'effacement prime sur une valeur
fournie simultanément. Sans observation, l'effacement rend la projection NULL.
L'interface graphique n'ajoute pas encore ces actions ni une vue de provenance.
Les nouvelles valeurs entrantes doivent être finies et non négatives ; les
anciennes données ne sont pas rejetées par une nouvelle contrainte SQL.

Le JSON conserve ses champs historiques et ajoute les compteurs ainsi qu'un
objet optionnel `progress` avec `manual`, `observed`, `observed_at`. Un import
récent restaure les deux couches ; sur une plateforme déjà présente, les valeurs
manuelles connues restent prioritaires et les observations sont actualisées selon
leur date. Un ancien JSON/CSV sans provenance traite ses valeurs comme manuelles
et garde le merge historique des imports (maximum d'heures). Le CSV reste un
export des valeurs effectives : il ne transporte pas la provenance. L'import d'un
statut explicite le considère comme manuel, par prudence, même s'il était calculé
chez l'émetteur. Une ancienne version du logiciel ignore l'extension JSON et ne
peut donc pas la restaurer intégralement.

Les sauvegardes applicatives utilisent un dump PostgreSQL complet, sans liste
fermée de tables : elles incluent les deux couches, leurs dates et les drapeaux.
Elles restent le moyen de restauration intégrale ; un export de collection ne
remplace pas une sauvegarde. La garde de version existante refuse une sauvegarde
dont la migration dépasse celle connue du binaire de restauration.

## Migration et retour arrière

Avant toute application future, arrêter les workers/écritures, créer une
sauvegarde complète avec la clé de chiffrement conservée séparément, puis tester
sa restauration sur une base jetable. Appliquer les migrations via le mécanisme
existant (`/migrate` ou `nexctl migrate`) et vérifier les valeurs avant de reprendre
les synchronisations. Aucun de ces gestes n'a été effectué sur TrueNAS.

Le retour le plus complet consiste à restaurer la sauvegarde prise avant migration
avec le binaire compatible ; il annule aussi les modifications postérieures à cette
sauvegarde. Pour conserver les valeurs effectives les plus récentes tout en
revenant au schéma précédent, arrêter toutes les écritures, conserver aussi un
dump **après** migration, puis exécuter depuis la racine du dépôt, sur la base
explicitement choisie, en une transaction :

```sql
BEGIN;
\i internal/db/migrations/20261029000001_progress_provenance.down.sql
DELETE FROM bun_migrations WHERE name = '20261029000001';
COMMIT;
```

Utiliser `psql -v ON_ERROR_STOP=1` et vérifier que cette migration est la dernière
appliquée (sinon retirer d'abord les migrations ultérieures). Le down conserve les
valeurs effectives dans les colonnes historiques mais supprime les observations
masquées et la provenance. Seul le dump après migration permet de les retrouver.
Redémarrer ensuite le binaire précédent avec les synchronisations désactivées :
il ne protège pas les valeurs manuelles. Ne pas supprimer de volumes.

## Limites assumées

Ce lot protège les chemins historiques ; il n'active pas le contrat
`progressadapter`, les comptes multiples, les succès individuels ou les sessions.
Une seule observation agrégée est sélectionnée par plateforme/boutique utilisateur ;
aucun historique de snapshots ni journal complet des éditions n'est créé. La date
historique disponible est `external_games.updated_at`, pas une horloge garantie du
fournisseur. Les appelants sans date utilisent l'heure du serveur ; les futurs
connecteurs devront fournir une vraie date de snapshot. La règle cumulative peut
masquer une correction à la baisse des heures par un fournisseur.

Les anciens champs ambigus sont volontairement figés jusqu'à une libération
explicite de leur override ; la migration ne prétend pas retrouver leur auteur.
Les scripts SQL externes qui modifient directement les projections doivent être
adaptés au domaine ; il n'y a pas de trigger ni de sécurité RLS pour des accès SQL
arbitraires. La sauvegarde/restauration et les migrations doivent être exécutées
en maintenance, sans ancien et nouveau binaire écrivant simultanément.

## Validation

Les tests ajoutés couvrent migration up/down, conservation de zéro/NULL et des
observations masquées, sync répétée, données absentes et anciennes, boutique NULL,
concurrence, isolation utilisateur/liens externes, statuts manuels, API et
export/import JSON, ainsi que sauvegarde/restauration des deux couches.

Résultats et commandes de validation sont consignés dans la PR 2A. Les suites
PostgreSQL utilisent uniquement les conteneurs jetables locaux. Aucun serveur
TrueNAS n'est contacté.

Vérifications dans cet environnement :

- `go test -count=1 -timeout 600s ./...` : seul échec connu dans
  `internal/logging/TestRoundTripper_LogsTransportError` (« expected transport
  error »), reproduit sur le worktree vierge de #1 au commit `90778ca`.
  Le proxy retourne une réponse pour le domaine `.invalid`. Les suites API,
  migrations, domaine, workers et sauvegardes passent.
- `go test -race -count=1 -timeout 600s ./internal/usergame ./internal/worker/tasks ./internal/api` : réussi.
- Sauvegardes : suite `internal/backup` avec `-v -count=1`, y compris
  `TestRestoreBackup`, réussie sans tests ignorés. Le système hôte ne possède pas
  `pg_dump`/`psql` : des wrappers temporaires hors dépôt acheminent les fichiers
  et arguments vers les vrais outils PostgreSQL 18 du conteneur de test local,
  identifié par son port publié. Cela vérifie le dump et la restauration réels,
  pas l'installation native des outils sur le futur hôte.
- Frontend : `npm test -- --maxWorkers=2`, 94 fichiers / 1 085 tests réussis ;
  `npm run build` réussi, TypeScript inclus. Avertissement existant de taille
  du bundle principal (environ 1 531 kB minifié).
- Format Go, Markdown du nouveau document et `git diff --check` vérifiés.

Le binaire Go utilisé est `/workspace/toolchains/go/bin/go`, avec un cache
inscriptible via `GOCACHE=/tmp/rk-go-cache`. L'exécutable `/usr/bin/go` de cet
environnement n'est pas le compilateur Go attendu. Les premiers essais bloqués
par cet exécutable/cache ne constituent pas des échecs de tests applicatifs.
L'ancienne attente de test qui remplaçait 10 heures ambiguës par 20 heures
externes a été corrigée : le test exige désormais 10 heures effectives **et**
20 heures dans l'observation séparée. Les nouvelles régressions détectées pendant
le développement ont été corrigées avant cette validation.
