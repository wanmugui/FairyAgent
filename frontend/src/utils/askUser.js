function normalizeOptions(options) {
  if (typeof options === 'string') {
    try {
      options = JSON.parse(options);
    } catch {
      options = [];
    }
  }
  return Array.isArray(options) ? options.filter(Boolean) : [];
}

export function normalizeAskQuestions(event) {
  let questions = event?.questions;
  if (typeof questions === 'string') {
    try {
      questions = JSON.parse(questions);
    } catch {
      questions = [];
    }
  }
  if (!Array.isArray(questions)) questions = [];

  if (!questions.length && Array.isArray(event?.options)) {
    questions = [{
      id: 'q1',
      question: event.question || event.title || '',
      options: event.options,
      allow_free_text: event.allow_free_text,
      multi_select: event.multi_select,
    }];
  }

  return questions
    .map((question, index) => {
      if (typeof question === 'string') {
        return { id: `q${index + 1}`, question, options: [] };
      }
      if (!question || typeof question !== 'object') return null;
      return {
        ...question,
        id: question.id || `q${index + 1}`,
        options: normalizeOptions(question.options),
      };
    })
    .filter(Boolean);
}
