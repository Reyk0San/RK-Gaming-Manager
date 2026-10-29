# Phase 2B — Progression Steam

## Configuration et utilisation

La connexion existante est réutilisée : renseigner une clé Steam Web API et le
SteamID64 dans **Sync → Steam**, puis lancer la synchronisation de bibliothèque.
La clé s'obtient sur <https://steamcommunity.com/dev/apikey>. Les identifiants
restent chiffrés par la configuration existante (`DB_ENCRYPTION_KEY`). Aucune
nouvelle variable ni authentification n'est nécessaire.

Le profil et les détails des jeux doivent être accessibles à l'API. La possession
d'une clé API et la connexion Steam ne donnent pas accès aux données privées.
Le serveur doit pouvoir joindre `api.steampowered.com` et les domaines déjà requis
par l'import Steam. Les icônes sont chargées depuis les CDN Steam autorisés.

Le bouton de synchronisation existant déclenche également la progression.
La fréquence `daily` ou `weekly` dans les réglages de synchronisation réutilise
le planificateur serveur existant ; `manual` désactive la planification.
La synchronisation périodique nécessite un serveur et ses workers actifs.
Les données sont affichées dans la fiche d'un jeu Steam lié à la bibliothèque.

## Données et garanties

- `IPlayerService/GetOwnedGames/v0001` fournit `playtime_forever` en minutes,
  converti en heures sans troncature. C'est un cumul par application, pas par OS.
- `ISteamUserStats/GetPlayerAchievements/v0001` fournit l'identifiant interne,
  l'état et éventuellement `unlocktime` (secondes Unix).
- `ISteamUserStats/GetSchemaForGame/v2` fournit titre, description et icônes.
  Les détails sont demandés en anglais ; Steam peut omettre une description,
  notamment pour les succès cachés. Aucun contenu absent n'est inventé.
- Le pourcentage observé est `100 × débloqués / total`. Il est inconnu si le
  total est inconnu ou nul. Ce calcul ne change jamais le statut manuel du jeu.
- Les succès sont demandés même pour un temps de jeu égal à zéro : ce zéro
  ne prouve pas l'absence de succès.

La migration `20261029000002_steam_progress` ajoute les snapshots séparés,
indexés par jeu externe et compte Steam, et l'identité du compte sélectionné
sur les observations de plateforme. Une empreinte interne des identifiants
chiffrés distingue les reconnexions ; elle n'est jamais renvoyée à l'interface.
Les valeurs manuelles, y compris zéro, restent prioritaires via la phase 2A.
Une nouvelle connexion ne supprime pas les anciens snapshots mais ne les
expose pas comme ceux du compte nouvellement connecté. Les compteurs observés
et le temps cumulé de comptes différents ne sont pas additionnés.

Une réponse privée, une erreur HTTP, un JSON invalide, une liste manquante,
un état manquant, des identifiants dupliqués ou un schéma incomplet ne remplacent
pas les derniers succès connus. Le temps et les succès ont des dates de
récupération distinctes : une nouvelle mesure du temps ne rafraîchit pas la date
des succès conservés. Les écritures sont idempotentes, rejettent les snapshots
plus anciens et contrôlent la propriété du jeu et les identifiants connectés.
La projection vérifie à nouveau la configuration sous verrou, dans la transaction
qui respecte les overrides manuels.

## API et pagination

Les API existantes conservent leur contrat. L'endpoint authentifié et en lecture
seule `GET /api/user-games/:id/steam-progress?offset=0&limit=50` ajoute les
observations Steam de l'utilisateur propriétaire : provenance, compte, dates,
heures, compteurs, pourcentage et détails des succès. `limit` vaut 50 par défaut,
avec un maximum de 100. `has_more` permet de charger la page suivante.
Les données ne sont disponibles que pour la connexion courante. Les compteurs
manuels/effectifs restent ceux des endpoints de plateformes existants.

Les trois endpoints Steam utilisés pour la progression ne proposent pas de
pagination officielle. La bibliothèque est obtenue intégralement, validée, puis
traitée par lots de dix jeux. L'API locale pagine la liste des succès pour
l'affichage. Les réponses HTTP sont bornées à 16 Mio et chaque nouvelle requête
est limitée à 30 secondes.

Les appels de progression sont limités à cinq requêtes par seconde par client.
Les erreurs réseau, HTTP 429 et 5xx déclenchent au maximum trois tentatives,
avec backoff exponentiel (1 puis 2 secondes) et respect de `Retry-After` numérique,
plafonné à une minute. L'API Store `appdetails` existante conserve sa reprise
interne et utilise désormais au maximum cinq tours côté adaptateur, avec backoff
exponentiel plafonné à une minute ; un échec persistant arrête la synchronisation
sans supprimer les observations. Le client HTTP de production a également un
timeout de 30 secondes. L'annulation interrompt les attentes. Ces limites locales
ne garantissent pas le respect d'un quota global partagé par plusieurs serveurs
ou clients : diminuer la fréquence en cas de quotas Steam récurrents.

Les lots déjà enregistrés restent conservés si une synchronisation est
interrompue. Une nouvelle synchronisation rejoue la bibliothèque, sans addition
ni duplication ; il n'existe pas de curseur Steam pour reprendre au dernier jeu.
Un échec isolé des succès ne bloque pas l'import du temps et de la bibliothèque ;
il sera retenté à la prochaine synchronisation manuelle ou périodique.

## Limites des API officielles

**Impossible à garantir avec les API officiellement accessibles :**

- Lire des jeux, du temps ou des succès privés/inaccessibles.
- Obtenir des succès pour un jeu sans succès Steam ou sans schéma exploitable ;
  une erreur ne permet pas de conclure qu'il y a zéro succès.
- Reconstituer une date de déblocage lorsque Steam renvoie zéro ou omet la date.
  Le champ reste `null`, même si le succès est débloqué.
- Récupérer une description ou une icône que Steam ne publie pas, ou traduire
  systématiquement les métadonnées de tous les jeux.
- Obtenir l'historique des sessions, une progression narrative, des statistiques
  uniformes propres à chaque jeu ou un flux temps réel des changements.
- Disposer de pagination, d'un curseur de reprise ou de changements incrémentaux
  sur ces endpoints. Le polling complet reste nécessaire.

Les observations individuelles sont une nouvelle table interne et ne modifient
pas le format d'export/import existant : elles sont récupérables par une nouvelle
synchronisation Steam. Les connecteurs PSN, GOG et Epic restent inchangés.

## Validation

Les tests Steam utilisent des réponses simulées, sans clé réelle : schéma et
état complets, données partielles/privées, zéros connus, dates absentes, mauvaises
identités, doublons, 429, annulation et erreurs sans secrets. Les tests PostgreSQL
couvrent migration aller-retour, idempotence, concurrence, rejet des snapshots
obsolètes, overrides zéro, pagination API, isolation et changement de connexion.
Les tests existants de la phase 2A conservent leurs vérifications des écritures
manuelles concurrentes et de non-régression des plateformes.
