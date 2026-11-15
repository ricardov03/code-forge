package queue

// B720: v8Sort is V8's Array.prototype.sort with a comparator (TimSort,
// third_party/v8/builtins/array-sort.tq), ported so the comparator sees the
// same calls in the same order: a comparator that throws for one receiver
// throws in Go exactly when it throws in Node. cmp returns the comparator's
// number (only its sign is read) or an error, which stops the sort.
func v8Sort[T any](arr []T, cmp func(a, b T) (int, error)) error {
	s := &v8SortState[T]{arr: arr, cmp: cmp, minGallop: 7}
	return s.run()
}

type v8SortState[T any] struct {
	arr       []T
	cmp       func(a, b T) (int, error)
	minGallop int
	runs      [][2]int // base, length
}

const v8MinGallopWins = 7

func (s *v8SortState[T]) run() error {
	n := len(s.arr)
	if n < 2 {
		return nil
	}
	remaining, low := n, 0
	minRun := v8MinRunLength(remaining)
	for remaining != 0 {
		current, err := s.countAndMakeRun(low, low+remaining)
		if err != nil {
			return err
		}
		if current < minRun {
			forced := min(minRun, remaining)
			if err := s.binaryInsertionSort(low, low+current, low+forced); err != nil {
				return err
			}
			current = forced
		}
		s.runs = append(s.runs, [2]int{low, current})
		if err := s.mergeCollapse(); err != nil {
			return err
		}
		low += current
		remaining -= current
	}
	return s.mergeForceCollapse()
}

func v8MinRunLength(n int) int {
	r := 0
	for n >= 64 {
		r |= n & 1
		n >>= 1
	}
	return n + r
}

func (s *v8SortState[T]) countAndMakeRun(lowArg, high int) (int, error) {
	low := lowArg + 1
	if low == high {
		return 1, nil
	}
	runLength := 2
	order, err := s.cmp(s.arr[low], s.arr[low-1])
	if err != nil {
		return 0, err
	}
	descending := order < 0
	previous := s.arr[low]
	for idx := low + 1; idx < high; idx++ {
		current := s.arr[idx]
		order, err = s.cmp(current, previous)
		if err != nil {
			return 0, err
		}
		if descending {
			if order >= 0 {
				break
			}
		} else if order < 0 {
			break
		}
		previous = current
		runLength++
	}
	if descending {
		for i, j := lowArg, lowArg+runLength-1; i < j; i, j = i+1, j-1 {
			s.arr[i], s.arr[j] = s.arr[j], s.arr[i]
		}
	}
	return runLength, nil
}

func (s *v8SortState[T]) binaryInsertionSort(low, startArg, high int) error {
	start := startArg
	if low == startArg {
		start++
	}
	for ; start < high; start++ {
		left, right := low, start
		pivot := s.arr[start]
		for left < right {
			mid := left + (right-left)>>1
			order, err := s.cmp(pivot, s.arr[mid])
			if err != nil {
				return err
			}
			if order < 0 {
				right = mid
			} else {
				left = mid + 1
			}
		}
		for p := start; p > left; p-- {
			s.arr[p] = s.arr[p-1]
		}
		s.arr[left] = pivot
	}
	return nil
}

func (s *v8SortState[T]) runInvariant(n int) bool {
	if n < 2 {
		return true
	}
	return s.runs[n-2][1] > s.runs[n-1][1]+s.runs[n][1]
}

func (s *v8SortState[T]) mergeCollapse() error {
	for len(s.runs) > 1 {
		n := len(s.runs) - 2
		if !s.runInvariant(n+1) || !s.runInvariant(n) {
			if s.runs[n-1][1] < s.runs[n+1][1] {
				n--
			}
			if err := s.mergeAt(n); err != nil {
				return err
			}
		} else if s.runs[n][1] <= s.runs[n+1][1] {
			if err := s.mergeAt(n); err != nil {
				return err
			}
		} else {
			break
		}
	}
	return nil
}

func (s *v8SortState[T]) mergeForceCollapse() error {
	for len(s.runs) > 1 {
		n := len(s.runs) - 2
		if n > 0 && s.runs[n-1][1] < s.runs[n+1][1] {
			n--
		}
		if err := s.mergeAt(n); err != nil {
			return err
		}
	}
	return nil
}

func (s *v8SortState[T]) mergeAt(i int) error {
	baseA, lengthA := s.runs[i][0], s.runs[i][1]
	baseB, lengthB := s.runs[i+1][0], s.runs[i+1][1]
	s.runs[i][1] = lengthA + lengthB
	s.runs = append(s.runs[:i+1], s.runs[i+2:]...)
	k, err := s.gallopRight(s.arr, s.arr[baseB], baseA, lengthA, 0)
	if err != nil {
		return err
	}
	baseA += k
	lengthA -= k
	if lengthA == 0 {
		return nil
	}
	lengthB, err = s.gallopLeft(s.arr, s.arr[baseA+lengthA-1], baseB, lengthB, lengthB-1)
	if err != nil {
		return err
	}
	if lengthB == 0 {
		return nil
	}
	if lengthA <= lengthB {
		return s.mergeLow(baseA, lengthA, baseB, lengthB)
	}
	return s.mergeHigh(baseA, lengthA, baseB, lengthB)
}

func (s *v8SortState[T]) gallopLeft(array []T, key T, base, length, hint int) (int, error) {
	lastOfs, offset := 0, 1
	order, err := s.cmp(array[base+hint], key)
	if err != nil {
		return 0, err
	}
	if order < 0 {
		maxOfs := length - hint
		for offset < maxOfs {
			order, err = s.cmp(array[base+hint+offset], key)
			if err != nil {
				return 0, err
			}
			if order >= 0 {
				break
			}
			lastOfs = offset
			offset = offset<<1 + 1
			if offset <= 0 {
				offset = maxOfs
			}
		}
		if offset > maxOfs {
			offset = maxOfs
		}
		lastOfs += hint
		offset += hint
	} else {
		maxOfs := hint + 1
		for offset < maxOfs {
			order, err = s.cmp(array[base+hint-offset], key)
			if err != nil {
				return 0, err
			}
			if order < 0 {
				break
			}
			lastOfs = offset
			offset = offset<<1 + 1
			if offset <= 0 {
				offset = maxOfs
			}
		}
		if offset > maxOfs {
			offset = maxOfs
		}
		tmp := lastOfs
		lastOfs = hint - offset
		offset = hint - tmp
	}
	lastOfs++
	for lastOfs < offset {
		m := lastOfs + (offset-lastOfs)>>1
		order, err = s.cmp(array[base+m], key)
		if err != nil {
			return 0, err
		}
		if order < 0 {
			lastOfs = m + 1
		} else {
			offset = m
		}
	}
	return offset, nil
}

func (s *v8SortState[T]) gallopRight(array []T, key T, base, length, hint int) (int, error) {
	lastOfs, offset := 0, 1
	order, err := s.cmp(key, array[base+hint])
	if err != nil {
		return 0, err
	}
	if order < 0 {
		maxOfs := hint + 1
		for offset < maxOfs {
			order, err = s.cmp(key, array[base+hint-offset])
			if err != nil {
				return 0, err
			}
			if order >= 0 {
				break
			}
			lastOfs = offset
			offset = offset<<1 + 1
			if offset <= 0 {
				offset = maxOfs
			}
		}
		if offset > maxOfs {
			offset = maxOfs
		}
		tmp := lastOfs
		lastOfs = hint - offset
		offset = hint - tmp
	} else {
		maxOfs := length - hint
		for offset < maxOfs {
			order, err = s.cmp(key, array[base+hint+offset])
			if err != nil {
				return 0, err
			}
			if order < 0 {
				break
			}
			lastOfs = offset
			offset = offset<<1 + 1
			if offset <= 0 {
				offset = maxOfs
			}
		}
		if offset > maxOfs {
			offset = maxOfs
		}
		lastOfs += hint
		offset += hint
	}
	lastOfs++
	for lastOfs < offset {
		m := lastOfs + (offset-lastOfs)>>1
		order, err = s.cmp(key, array[base+m])
		if err != nil {
			return 0, err
		}
		if order < 0 {
			offset = m
		} else {
			lastOfs = m + 1
		}
	}
	return offset, nil
}

func (s *v8SortState[T]) mergeLow(baseA, lengthA, baseB, lengthB int) error {
	work := s.arr
	temp := make([]T, lengthA)
	copy(temp, work[baseA:baseA+lengthA])
	dest, cursorTemp, cursorB := baseA, 0, baseB
	work[dest] = work[cursorB]
	dest++
	cursorB++
	succeed := func() {
		if lengthA > 0 {
			copy(work[dest:dest+lengthA], temp[cursorTemp:cursorTemp+lengthA])
		}
	}
	copyB := func() {
		copy(work[dest:dest+lengthB], work[cursorB:cursorB+lengthB])
		work[dest+lengthB] = temp[cursorTemp]
	}
	lengthB--
	if lengthB == 0 {
		succeed()
		return nil
	}
	if lengthA == 1 {
		copyB()
		return nil
	}
	minGallop := s.minGallop
	for {
		winsA, winsB := 0, 0
		for {
			order, err := s.cmp(work[cursorB], temp[cursorTemp])
			if err != nil {
				return err
			}
			if order < 0 {
				work[dest] = work[cursorB]
				dest++
				cursorB++
				winsB++
				lengthB--
				winsA = 0
				if lengthB == 0 {
					succeed()
					return nil
				}
				if winsB >= minGallop {
					break
				}
			} else {
				work[dest] = temp[cursorTemp]
				dest++
				cursorTemp++
				winsA++
				lengthA--
				winsB = 0
				if lengthA == 1 {
					copyB()
					return nil
				}
				if winsA >= minGallop {
					break
				}
			}
		}
		minGallop++
		first := true
		for winsA >= v8MinGallopWins || winsB >= v8MinGallopWins || first {
			first = false
			minGallop = max(1, minGallop-1)
			s.minGallop = minGallop
			k, err := s.gallopRight(temp, work[cursorB], cursorTemp, lengthA, 0)
			if err != nil {
				return err
			}
			winsA = k
			if winsA > 0 {
				copy(work[dest:dest+winsA], temp[cursorTemp:cursorTemp+winsA])
				dest += winsA
				cursorTemp += winsA
				lengthA -= winsA
				if lengthA == 1 {
					copyB()
					return nil
				}
				if lengthA == 0 {
					succeed()
					return nil
				}
			}
			work[dest] = work[cursorB]
			dest++
			cursorB++
			lengthB--
			if lengthB == 0 {
				succeed()
				return nil
			}
			k, err = s.gallopLeft(work, temp[cursorTemp], cursorB, lengthB, 0)
			if err != nil {
				return err
			}
			winsB = k
			if winsB > 0 {
				copy(work[dest:dest+winsB], work[cursorB:cursorB+winsB])
				dest += winsB
				cursorB += winsB
				lengthB -= winsB
				if lengthB == 0 {
					succeed()
					return nil
				}
			}
			work[dest] = temp[cursorTemp]
			dest++
			cursorTemp++
			lengthA--
			if lengthA == 1 {
				copyB()
				return nil
			}
		}
		minGallop++
		s.minGallop = minGallop
	}
}

func (s *v8SortState[T]) mergeHigh(baseA, lengthA, baseB, lengthB int) error {
	work := s.arr
	temp := make([]T, lengthB)
	copy(temp, work[baseB:baseB+lengthB])
	dest, cursorTemp, cursorA := baseB+lengthB-1, lengthB-1, baseA+lengthA-1
	work[dest] = work[cursorA]
	dest--
	cursorA--
	succeed := func() {
		if lengthB > 0 {
			copy(work[dest-(lengthB-1):dest+1], temp[0:lengthB])
		}
	}
	copyA := func() {
		dest -= lengthA
		cursorA -= lengthA
		copy(work[dest+1:dest+1+lengthA], work[cursorA+1:cursorA+1+lengthA])
		work[dest] = temp[cursorTemp]
	}
	lengthA--
	if lengthA == 0 {
		succeed()
		return nil
	}
	if lengthB == 1 {
		copyA()
		return nil
	}
	minGallop := s.minGallop
	for {
		winsA, winsB := 0, 0
		for {
			order, err := s.cmp(temp[cursorTemp], work[cursorA])
			if err != nil {
				return err
			}
			if order < 0 {
				work[dest] = work[cursorA]
				dest--
				cursorA--
				winsA++
				lengthA--
				winsB = 0
				if lengthA == 0 {
					succeed()
					return nil
				}
				if winsA >= minGallop {
					break
				}
			} else {
				work[dest] = temp[cursorTemp]
				dest--
				cursorTemp--
				winsB++
				lengthB--
				winsA = 0
				if lengthB == 1 {
					copyA()
					return nil
				}
				if winsB >= minGallop {
					break
				}
			}
		}
		minGallop++
		first := true
		for winsA >= v8MinGallopWins || winsB >= v8MinGallopWins || first {
			first = false
			minGallop = max(1, minGallop-1)
			s.minGallop = minGallop
			k, err := s.gallopRight(work, temp[cursorTemp], baseA, lengthA, lengthA-1)
			if err != nil {
				return err
			}
			winsA = lengthA - k
			if winsA > 0 {
				dest -= winsA
				cursorA -= winsA
				copy(work[dest+1:dest+1+winsA], work[cursorA+1:cursorA+1+winsA])
				lengthA -= winsA
				if lengthA == 0 {
					succeed()
					return nil
				}
			}
			work[dest] = temp[cursorTemp]
			dest--
			cursorTemp--
			lengthB--
			if lengthB == 1 {
				copyA()
				return nil
			}
			k, err = s.gallopLeft(temp, work[cursorA], 0, lengthB, lengthB-1)
			if err != nil {
				return err
			}
			winsB = lengthB - k
			if winsB > 0 {
				dest -= winsB
				cursorTemp -= winsB
				copy(work[dest+1:dest+1+winsB], temp[cursorTemp+1:cursorTemp+1+winsB])
				lengthB -= winsB
				if lengthB == 1 {
					copyA()
					return nil
				}
				if lengthB == 0 {
					succeed()
					return nil
				}
			}
			work[dest] = work[cursorA]
			dest--
			cursorA--
			lengthA--
			if lengthA == 0 {
				succeed()
				return nil
			}
		}
		minGallop++
		s.minGallop = minGallop
	}
}
