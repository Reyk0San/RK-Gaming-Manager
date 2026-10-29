import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';

interface SteamProgress {
  source: 'steam';
  available: boolean;
  account_id?: string;
  observed_at?: string;
  achievements_observed_at?: string | null;
  achievements_unlocked?: number | null;
  achievements_total?: number | null;
  completion_percent?: number | null;
  has_more?: boolean;
  achievements: {
    id: string;
    title: string;
    description: string;
    icon: string;
    icon_gray: string;
    unlocked: boolean;
    unlocked_at: string | null;
  }[];
}

export function SteamAchievements({ gameId }: { gameId: string }) {
  const [offset, setOffset] = useState(0);
  const { data, isPending, isError } = useQuery({
    queryKey: ['steam-progress', gameId, offset],
    queryFn: () =>
      api.get<SteamProgress>(`/user-games/${gameId}/steam-progress`, {
        params: { offset, limit: 50 },
      }),
  });
  if (isPending) return <p role="status">Loading Steam achievements…</p>;
  if (isError) return <p role="status">Steam achievements could not be loaded.</p>;
  return (
    <section aria-label="Steam achievements" className="space-y-3">
      <h3 className="font-medium">Steam achievements</h3>
      <p className="text-sm text-muted-foreground">
        Source: Steam. These observations are separate from your manual values.
      </p>
      {!data.available || !data.achievements_observed_at ? (
        <p>Achievements unavailable. Previously saved progress is preserved.</p>
      ) : (
        <>
          <p>
            {data.achievements_unlocked ?? '—'} / {data.achievements_total ?? '—'}
            {data.completion_percent != null && ` (${data.completion_percent.toFixed(1)}%)`}
          </p>
          <p className="text-sm">
            Last successful achievement sync:{' '}
            {new Date(data.achievements_observed_at).toLocaleString()}
          </p>
          <ul className="space-y-2">
            {data.achievements.map((achievement) => (
              <li key={achievement.id} className="flex gap-3">
                {(achievement.unlocked ? achievement.icon : achievement.icon_gray) && (
                  <img
                    src={achievement.unlocked ? achievement.icon : achievement.icon_gray}
                    alt=""
                    width={48}
                    height={48}
                    loading="lazy"
                    referrerPolicy="no-referrer"
                  />
                )}
                <div>
                  <p>
                    {achievement.title || achievement.id} —{' '}
                    {achievement.unlocked ? 'Unlocked' : 'Locked'}
                  </p>
                  <p className="text-sm text-muted-foreground">{achievement.description}</p>
                  {achievement.unlocked_at && (
                    <time dateTime={achievement.unlocked_at}>
                      {new Date(achievement.unlocked_at).toLocaleString()}
                    </time>
                  )}
                </div>
              </li>
            ))}
          </ul>
          <div className="flex gap-2">
            <Button
              variant="outline"
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - 50))}
            >
              Previous achievements
            </Button>
            <Button
              variant="outline"
              disabled={!data.has_more}
              onClick={() => setOffset(offset + 50)}
            >
              Next achievements
            </Button>
          </div>
        </>
      )}
    </section>
  );
}
