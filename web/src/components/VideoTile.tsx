import { useEffect, useRef } from 'react';
import type { CallVideo } from '../voice';

export function VideoTile({ video }: { video: CallVideo }) {
  const element = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    const target = element.current;
    if (!target) return;
    video.track.attach(target);
    return () => { video.track.detach(target); };
  }, [video.track]);
  return <figure className={`callVideo ${video.local ? 'localVideo' : ''}`}><video ref={element} autoPlay playsInline muted aria-label={video.local ? 'Your camera preview' : `${video.name} camera`} /><figcaption>{video.local ? 'You' : video.name}</figcaption></figure>;
}
