package proc

// identity tells one process from another that happens to get the same pid
// later. It returns a string that stays the same for the life of a process and
// differs for any later process with that pid, and false when there is no such
// process (or it is a zombie). The implementations are per platform.
