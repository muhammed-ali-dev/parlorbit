import { ArrowRightIcon, HouseIcon } from '@phosphor-icons/react';
import { Link } from 'react-router-dom';
export type HouseSummary = { id: string; name: string; roomCount: number; memberCount: number; lastRoomId: string };
export function HouseCard({ house }: { house: HouseSummary }) {
 const tone = [...house.id].reduce((sum, char) => sum + char.charCodeAt(0), 0) % 3;
 return <Link className={`nightCard nightTone-${tone}`} to={`/houses/${house.id}${house.lastRoomId ? `/rooms/${house.lastRoomId}` : ''}`}>
  <div className="nightCover" aria-hidden="true"><HouseIcon size={36} weight="light" /><span>{house.name.slice(0,1).toUpperCase()}</span><div className="coverTiles">{Array.from({length:9},(_,i)=><i key={i}/>)}</div></div>
  <div className="nightCardBody"><h3>{house.name}</h3><p>{house.memberCount} {house.memberCount===1?'member':'members'} · {house.roomCount} {house.roomCount===1?'room':'rooms'}</p><span>Open House <ArrowRightIcon size={16}/></span></div>
 </Link>;
}
