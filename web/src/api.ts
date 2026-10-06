import { APIError, type Bootstrap, type HouseSnapshot, type InvitePreview, type RoomKind } from './types';

const request = async <T>(path: string, init?: RequestInit): Promise<T> => {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...init,
    headers: init?.body ? { 'Content-Type': 'application/json', ...init.headers } : init?.headers,
  });
  const payload = await response.json().catch(() => null);
  if (!response.ok) {
    const error = payload?.error;
    throw new APIError(error?.message ?? 'Parlorbit could not finish that request.', error?.code, error?.requestId);
  }
  return payload.data as T;
};

export const api = {
  ensureSession: () => request<{ sessionId: string; expiresAt: number }>('/api/v1/guest-session', { method: 'POST' }),
  bootstrap: () => request<Bootstrap>('/api/v1/bootstrap'),
  house: (houseId: string) => request<HouseSnapshot>(`/api/v1/houses/${houseId}`),
  createHouse: (name: string, displayName: string) =>
    request<{ house: HouseSnapshot; inviteToken: string }>('/api/v1/houses', {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify({ name, displayName }),
    }),
  updateHouse: (houseId: string, name: string) => request<HouseSnapshot>(`/api/v1/houses/${houseId}`, { method: 'PATCH', body: JSON.stringify({ name }) }),
  deleteHouse: (houseId: string) => request(`/api/v1/houses/${houseId}`, { method: 'DELETE' }),
  transferHost: (houseId: string, memberId: string) => request<HouseSnapshot>(`/api/v1/houses/${houseId}/host-transfer`, { method: 'POST', body: JSON.stringify({ memberId }) }),
  createRoom: (houseId: string, name: string, kind: RoomKind) => request<HouseSnapshot>(`/api/v1/houses/${houseId}/rooms`, { method: 'POST', body: JSON.stringify({ name, kind }) }),
  updateRoom: (houseId: string, roomId: string, name: string, kind: RoomKind) => request<HouseSnapshot>(`/api/v1/houses/${houseId}/rooms/${roomId}`, { method: 'PATCH', body: JSON.stringify({ name, kind }) }),
  deleteRoom: (houseId: string, roomId: string) => request<HouseSnapshot>(`/api/v1/houses/${houseId}/rooms/${roomId}`, { method: 'DELETE' }),
  invitePreview: (token: string) => request<InvitePreview>(`/api/v1/invites/${encodeURIComponent(token)}`),
  requestJoin: (token: string, displayName: string) => request<{ id: string; status: string; expiresAt: number }>(`/api/v1/invites/${encodeURIComponent(token)}/requests`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ displayName }) }),
  joinStatus: (requestId: string) => request<{ id: string; houseId: string; roomId?: string; status: string; expiresAt: number }>(`/api/v1/join-requests/${requestId}`),
  decideJoin: (houseId: string, requestId: string, decision: 'approve' | 'decline') => request(`/api/v1/houses/${houseId}/join-requests/${requestId}/decision`, { method: 'POST', body: JSON.stringify({ decision }) }),
  rotateInvite: (houseId: string) => request<{ inviteToken: string }>(`/api/v1/houses/${houseId}/invite/rotate`, { method: 'POST', body: '{}' }),
  removeMember: (houseId: string, memberId: string) => request(`/api/v1/houses/${houseId}/members/${memberId}`, { method: 'DELETE' }),
  setGame: (houseId: string, roomId: string, url: string, expectedRevision: number) => request<HouseSnapshot>(`/api/v1/houses/${houseId}/rooms/${roomId}/game`, { method: 'PUT', body: JSON.stringify({ url, expectedRevision }) }),
  clearGame: (houseId: string, roomId: string, expectedRevision: number) => request<HouseSnapshot>(`/api/v1/houses/${houseId}/rooms/${roomId}/game?expectedRevision=${expectedRevision}`, { method: 'DELETE' }),
  transferCoordinator: (houseId: string, roomId: string, memberId: string, expectedRevision: number) => request<HouseSnapshot>(`/api/v1/houses/${houseId}/rooms/${roomId}/coordinator-transfer`, { method: 'POST', body: JSON.stringify({ memberId, expectedRevision }) }),
  voiceToken: (houseId: string, roomId: string) => request<{ url: string; token: string; mediaGeneration: number }>(`/api/v1/houses/${houseId}/rooms/${roomId}/voice-token`, { method: 'POST', body: '{}' }),
};
