// Shared mutable playback state; modules read and write the same session.
export const s = {
  audio: null, queue: [], currentIndex: -1, isPlaying: false, loopMode: 'off',
  shuffleOn: false, lyrics: [], activeLyricIndex: -1,
  isDraggingProgress: false, playHistory: [], playToken: 0, failedTrackId: null
};
