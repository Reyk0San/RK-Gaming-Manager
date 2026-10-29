import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { describe, it, expect, vi } from 'vitest';
import { api } from '@/api/client';
import { SteamAchievements } from './steam-achievements';

vi.mock('@/api/client', () => ({ api: { get: vi.fn() } }));
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SteamAchievements gameId="game" />
    </QueryClientProvider>,
  );
}
describe('Steam achievements', () => {
  it('shows known zero counts, source, date, details and pagination', async () => {
    vi.mocked(api.get).mockResolvedValue({
      available: true,
      achievements_unlocked: 0,
      achievements_total: 1,
      completion_percent: 0,
      achievements_observed_at: '2026-10-29T12:00:00Z',
      has_more: true,
      achievements: [
        {
          id: 'A',
          title: 'First win',
          description: 'Win a game',
          unlocked: false,
          unlocked_at: null,
          icon: '',
          icon_gray: '',
        },
      ],
    });
    mount();
    expect(await screen.findByText('First win — Locked')).toBeInTheDocument();
    expect(screen.getByText('0 / 1 (0.0%)')).toBeInTheDocument();
    expect(screen.getByText(/Source: Steam/)).toBeInTheDocument();
    expect(screen.getByText(/Last successful achievement sync/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Next achievements' }));
    expect(api.get).toHaveBeenLastCalledWith('/user-games/game/steam-progress', {
      params: { offset: 50, limit: 50 },
    });
  });
  it('keeps unavailable distinct from known zero', async () => {
    vi.mocked(api.get).mockResolvedValue({ available: false, achievements: [] });
    mount();
    expect(await screen.findByText(/Achievements unavailable/)).toBeInTheDocument();
    expect(screen.queryByText(/0.0%/)).not.toBeInTheDocument();
  });
  it('reports API failures', async () => {
    vi.mocked(api.get).mockRejectedValue(new Error('offline'));
    mount();
    expect(await screen.findByText(/could not be loaded/)).toBeInTheDocument();
  });
});
