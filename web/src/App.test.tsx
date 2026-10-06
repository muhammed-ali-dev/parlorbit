import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, expect, test, vi } from 'vitest';
import App from './App';

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith('/guest-session')) return new Response(JSON.stringify({ data: { sessionId: 'ses_test', expiresAt: 1 } }), { status: 201 });
    if (url.endsWith('/bootstrap')) return new Response(JSON.stringify({ data: { sessionId: 'ses_test', houses: [] } }), { status: 200 });
    return new Response(JSON.stringify({ error: { code: 'not_found', message: 'Missing' } }), { status: 404 });
  }));
});

test('shows a useful empty House state for a new browser', async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><MemoryRouter><App /></MemoryRouter></QueryClientProvider>);
  expect(await screen.findByText('Game night starts here.')).toBeInTheDocument();
  expect(screen.getByText('A House for your next game night.')).toBeInTheDocument();
});
