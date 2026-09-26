// Shared mutable playback state; modules read and write the same session.
export const s = {
  audio: null, queue: [], currentIndex: -1, isPlaying: false, loopMode: 'all',
  shuffleOn: false, lyrics: [], activeLyricIndex: -1, lastScrobbledTrackId: null,
  isDraggingProgress: false, playHistory: []
};
