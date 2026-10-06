import { useCallback, useEffect, useRef, useState } from 'react';
import { Room, RoomEvent, Track, type LocalVideoTrack, type RemoteVideoTrack } from 'livekit-client';
import { api } from './api';

export type VoiceState = 'idle' | 'connecting' | 'connected' | 'listen-only' | 'reconnecting' | 'error';
export type CallVideo = { id: string; name: string; local: boolean; track: LocalVideoTrack | RemoteVideoTrack };

export function useVoice(houseId: string, roomId: string, enabled = true) {
  const [desired, setDesired] = useState(false);
  const [state, setState] = useState<VoiceState>('idle');
  const [muted, setMuted] = useState(false);
  const mutedRef = useRef(false);
  const microphoneDenied = useRef(false);
  const [message, setMessage] = useState('Voice is optional.');
  const [attempt, setAttempt] = useState(0);
  const [needsAudio, setNeedsAudio] = useState(false);
  const [videos, setVideos] = useState<CallVideo[]>([]);
  const [cameraEnabled, setCameraEnabled] = useState(false);
  const [cameraBusy, setCameraBusy] = useState(false);
  const [cameraError, setCameraError] = useState('');
  const [microphoneBusy, setMicrophoneBusy] = useState(false);
  const cameraChanging = useRef(false);
  const microphoneChanging = useRef(false);
  const liveRoom = useRef<Room | null>(null);
  const audioRoot = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    setVideos([]); setCameraEnabled(false); setCameraError(''); setNeedsAudio(false);
    if (!desired) return;
    if (!enabled) { setState('reconnecting'); setMessage('Waiting to reconnect to the House…'); return; }
    let cancelled = false;
    microphoneDenied.current = false;
    const room = new Room({ adaptiveStream: true, dynacast: true });
    liveRoom.current = room;
    const attachedAudio = new Map<Track, HTMLMediaElement>();
    const syncVideo = () => {
      if (cancelled) return;
      const next: CallVideo[] = [];
      const participants = [room.localParticipant, ...room.remoteParticipants.values()];
      for (const participant of participants) {
        const publication = participant.getTrackPublication(Track.Source.Camera);
        if (publication?.track && !publication.isMuted && publication.track.kind === Track.Kind.Video) {
          next.push({ id: participant.identity, name: participant.name || 'Friend', local: participant === room.localParticipant, track: publication.track as LocalVideoTrack | RemoteVideoTrack });
        }
      }
      setVideos(next);
      setCameraEnabled(room.localParticipant.isCameraEnabled);
    };
    const connected = () => {
      if (cancelled) return;
      setState(microphoneDenied.current ? 'listen-only' : 'connected');
      setMessage(microphoneDenied.current ? 'Listening only. Allow microphone access to speak.' : mutedRef.current ? 'Voice connected · muted.' : 'Voice connected.');
      syncVideo();
    };
    room.on(RoomEvent.TrackSubscribed, track => {
      if (!cancelled && track.kind === Track.Kind.Audio && audioRoot.current) {
        attachedAudio.get(track)?.remove();
        const element = track.attach();
        attachedAudio.set(track, element);
        audioRoot.current.appendChild(element);
      }
      syncVideo();
    });
    room.on(RoomEvent.TrackUnsubscribed, track => {
      // LiveKit may already have detached the track before emitting this event.
      // Remove the element we own even when track.detach() returns an empty list.
      attachedAudio.get(track)?.remove(); attachedAudio.delete(track);
      track.detach().forEach(element => element.remove()); syncVideo();
    });
    room.on(RoomEvent.LocalTrackPublished, syncVideo);
    room.on(RoomEvent.LocalTrackUnpublished, syncVideo);
    room.on(RoomEvent.TrackMuted, syncVideo);
    room.on(RoomEvent.TrackUnmuted, syncVideo);
    room.on(RoomEvent.ParticipantDisconnected, syncVideo);
    room.on(RoomEvent.Reconnecting, () => { if (!cancelled) { setState('reconnecting'); setMessage('Voice is reconnecting…'); } });
    room.on(RoomEvent.Reconnected, connected);
    room.on(RoomEvent.Disconnected, () => {
      if (!cancelled) { setState('error'); setMessage('Call disconnected. Try joining again.'); setVideos([]); setCameraEnabled(false); }
    });
    room.on(RoomEvent.AudioPlaybackStatusChanged, () => { if (!cancelled) setNeedsAudio(!room.canPlaybackAudio); });
    const connect = async () => {
      setState('connecting'); setMessage('Connecting to the call…');
      try {
        const credentials = await api.voiceToken(houseId, roomId);
        if (cancelled) return;
        await room.connect(credentials.url, credentials.token);
        if (cancelled) { await room.disconnect(); return; }
        try { await room.localParticipant.setMicrophoneEnabled(!mutedRef.current); }
        catch { microphoneDenied.current = true; }
        if (cancelled) { await room.disconnect(); return; }
        connected();
        // Joining is a user gesture, but browsers may still require another one for sound.
        await room.startAudio().catch(() => { if (!cancelled) setNeedsAudio(true); });
        if (!cancelled) setNeedsAudio(!room.canPlaybackAudio);
      } catch (error) {
        if (!cancelled) { setState('error'); setMessage(error instanceof Error ? error.message : 'Voice could not connect.'); }
        await room.disconnect();
      }
    };
    void connect();
    return () => {
      cancelled = true;
      if (liveRoom.current === room) liveRoom.current = null;
      room.removeAllListeners();
      // Stop devices synchronously rather than waiting for the signaling disconnect.
      for (const publication of room.localParticipant.trackPublications.values()) publication.track?.stop();
      void room.disconnect();
      for (const element of attachedAudio.values()) element.remove();
      attachedAudio.clear();
      audioRoot.current?.replaceChildren();
    };
  }, [desired, houseId, roomId, enabled, attempt]);

  const join = useCallback(() => { setDesired(true); setAttempt(value => value + 1); }, []);
  const leave = useCallback(() => {
    for (const publication of liveRoom.current?.localParticipant.trackPublications.values() ?? []) publication.track?.stop();
    setDesired(false); setState('idle'); setMessage('Voice is optional.'); setNeedsAudio(false); setVideos([]); setCameraEnabled(false); setCameraError('');
  }, []);
  const toggleMute = useCallback(async () => {
    const room = liveRoom.current;
    if (!room || microphoneChanging.current) return;
    microphoneChanging.current = true; setMicrophoneBusy(true);
    const next = state === 'listen-only' ? false : !mutedRef.current;
    try {
      await room.localParticipant.setMicrophoneEnabled(!next);
      if (liveRoom.current !== room) { await room.localParticipant.setMicrophoneEnabled(false); return; }
      microphoneDenied.current = false; mutedRef.current = next; setMuted(next); setState('connected');
      setMessage(next ? 'Voice connected · muted.' : 'Voice connected.');
    } catch { if (liveRoom.current === room) { microphoneDenied.current = true; setState('listen-only'); setMessage('Listening only. Allow microphone access to speak.'); } }
    finally { microphoneChanging.current = false; setMicrophoneBusy(false); }
  }, [state]);
  const toggleCamera = useCallback(async () => {
    const room = liveRoom.current;
    if (!room || cameraChanging.current || !['connected', 'listen-only'].includes(state)) return;
    cameraChanging.current = true; setCameraBusy(true); setCameraError('');
    try {
      await room.localParticipant.setCameraEnabled(!room.localParticipant.isCameraEnabled, { resolution: { width: 640, height: 360, frameRate: 24 } });
      if (liveRoom.current !== room) { await room.localParticipant.setCameraEnabled(false); return; }
      setCameraEnabled(room.localParticipant.isCameraEnabled);
    } catch {
      if (liveRoom.current === room) setCameraError('Camera unavailable. Allow camera access and check that another app is not using it. Voice stays connected.');
    } finally { cameraChanging.current = false; setCameraBusy(false); }
  }, [state]);
  const enableAudio = useCallback(async () => { try { await liveRoom.current?.startAudio(); setNeedsAudio(false); } catch { setNeedsAudio(true); } }, []);
  return { state, muted, message, desired, join, leave, toggleMute, audioRoot, needsAudio, enableAudio, videos, cameraEnabled, cameraBusy, cameraError, toggleCamera, microphoneBusy };
}
