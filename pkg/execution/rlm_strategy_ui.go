package execution

// SetProgressReporter attaches a progress reporter for iteration updates.
func (s *RLMStrategy) SetProgressReporter(reporter ProgressReporter) {
	if s == nil {
		return
	}
	adapter := s.ensureStreamAdapter()
	if adapter == nil {
		return
	}
	adapter.SetProgressReporter(reporter)
}

// SetToastNotifier attaches a toast notifier for budget warnings.
func (s *RLMStrategy) SetToastNotifier(notifier ToastNotifier) {
	if s == nil {
		return
	}
	adapter := s.ensureStreamAdapter()
	if adapter == nil {
		return
	}
	adapter.SetToastNotifier(notifier)
}
